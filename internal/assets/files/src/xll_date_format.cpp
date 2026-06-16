#include "xll_date_format.h"
#include "xll_excel.h"          // xll::CallExcel
#include "xll_greedy_mesh.h"    // xll::mesh::GreedyMesh (greedy-voxel coalesce)
#include "types/ScopedXLOPER12.h"
#include "types/utility.h"      // PascalToWString

#include "com/excel_app.h"      // xll::com::AcquireExcelApplication
#include "com/dispatch_helpers.h" // xll::com::GetProperty / Invoke

#include <ole2.h>
#include <map>
#include <utility>

namespace xll {

PendingDateFormats& PendingDateFormats::Instance() {
    static PendingDateFormats inst;
    return inst;
}

void PendingDateFormats::Enqueue(const std::vector<PendingFormat>& items) {
    if (items.empty()) return;
    std::lock_guard<std::mutex> lock(m_mutex);
    m_pending.insert(m_pending.end(), items.begin(), items.end());
}

std::vector<PendingFormat> PendingDateFormats::Drain() {
    std::lock_guard<std::mutex> lock(m_mutex);
    std::vector<PendingFormat> out;
    out.swap(m_pending);
    return out;
}

bool PendingDateFormats::HasPending() {
    std::lock_guard<std::mutex> lock(m_mutex);
    return !m_pending.empty();
}

bool PendingDateFormats::AlreadyFormatted(IDSHEET idSheet, int row, int col) {
    std::lock_guard<std::mutex> lock(m_mutex);
    return m_formatted.count(std::make_tuple(idSheet, row, col)) != 0;
}

void PendingDateFormats::MarkFormatted(IDSHEET idSheet, int row, int col) {
    std::lock_guard<std::mutex> lock(m_mutex);
    m_formatted.insert(std::make_tuple(idSheet, row, col));
}

// Shared back half of the producer: given date cells already collected on the
// calc thread, capture xlfCaller as the anchor and enqueue one PendingFormat
// per cell. No-op when `cells` is empty; NEVER throws.
static void EnqueueDateFormatsForCaller(const std::vector<DateCell>& cells) {
    try {
        if (cells.empty()) return;

        ScopedXLOPER12 xCaller;
        if (xll::CallExcel(xlfCaller, xCaller) != xlretSuccess) return;

        IDSHEET idSheet = 0;
        XLREF12 anchor{};
        const DWORD t = xCaller.get()->xltype & ~(xlbitDLLFree | xlbitXLFree);
        if (t & xltypeSRef) {
            anchor = xCaller.get()->val.sref.ref;
            // SRef is relative to the calling cell's sheet and carries no
            // idSheet. Resolve the concrete IDSHEET now (on the calc thread,
            // where it is legal) so the CalculationEnded drain targets the
            // right sheet even if the active sheet changed by then. No-arg
            // xlSheetId returns an xltypeRef for the active sheet; mirrors
            // LookupSheetName in types/src/converters.cpp.
            ScopedXLOPER12Result xSheetId;
            if (xll::CallExcel(xlSheetId, xSheetId) == xlretSuccess &&
                ((xSheetId.get()->xltype & ~(xlbitDLLFree | xlbitXLFree)) & xltypeRef)) {
                idSheet = xSheetId.get()->val.mref.idSheet;
            }
            // If resolution fails, idSheet stays 0: the item is still enqueued
            // with key (0,row,col), but an idSheet==0 ref does not resolve to a
            // real sheet name via xlSheetNm in the drain, so the COM target
            // build fails -> the cell is NOT marked -> it self-heals on the next
            // recalc rather than formatting the wrong sheet. (xlSheetId failing
            // on a live calc thread is near-impossible, so this path is
            // effectively dead.)
        } else if ((t & xltypeRef) && xCaller.get()->val.mref.lpmref &&
                   xCaller.get()->val.mref.lpmref->count > 0) {
            idSheet = xCaller.get()->val.mref.idSheet;
            anchor = xCaller.get()->val.mref.lpmref->reftbl[0];
        } else {
            return;
        }

        std::vector<PendingFormat> items;
        items.reserve(cells.size());
        for (const auto& c : cells) {
            const int row = anchor.rwFirst + c.rowOff;
            const int col = anchor.colFirst + c.colOff;
            // Once-per-cell: never re-enqueue a cell the drain has already
            // formatted, so the pending vector stays tiny across recalcs.
            if (PendingDateFormats::Instance().AlreadyFormatted(idSheet, row, col))
                continue;
            PendingFormat pf;
            pf.idSheet = idSheet;
            pf.ref.rwFirst = pf.ref.rwLast = row;
            pf.ref.colFirst = pf.ref.colLast = col;
            pf.format = c.format;
            items.push_back(std::move(pf));
        }
        PendingDateFormats::Instance().Enqueue(items);
    } catch (...) { /* never throw into the wrapper */ }
}

void ScheduleDateFormatsForCaller(const protocol::Any* result) {
    try {
        std::vector<DateCell> cells;
        CollectDateCells(result, cells);
        EnqueueDateFormatsForCaller(cells);
    } catch (...) { /* never throw into the wrapper */ }
}

void ScheduleDateFormatsForCaller(const protocol::Grid* grid) {
    try {
        // Sync grid wrappers hand us a bare Grid (no Any wrapper). Reuse the
        // Grid overload of CollectDateCells so date-format derivation stays
        // centralized in types/converters.cpp.
        std::vector<DateCell> cells;
        CollectDateCells(grid, cells);
        EnqueueDateFormatsForCaller(cells);
    } catch (...) { /* never throw into the wrapper */ }
}

namespace {

// 1-based column number -> column letters ("A", "Z", "AA", "AA"...). Excel
// columns are bijective base-26 (no zero digit). col must be >= 1.
std::wstring ColumnLetters(int col) {
    std::wstring out;
    while (col > 0) {
        int rem = (col - 1) % 26;
        out.insert(out.begin(), static_cast<wchar_t>(L'A' + rem));
        col = (col - 1) / 26;
    }
    return out;
}

// XLREF12 (0-based rwFirst/rwLast/colFirst/colLast) -> an A1 range string with
// absolute markers, e.g. "$A$1" for a single cell or "$A$1:$C$21" for a block.
std::wstring BuildA1(const XLREF12& r) {
    const std::wstring c1 = ColumnLetters(r.colFirst + 1);
    const std::wstring c2 = ColumnLetters(r.colLast + 1);
    std::wstring a1 = L"$" + c1 + L"$" + std::to_wstring(r.rwFirst + 1);
    if (r.rwFirst == r.rwLast && r.colFirst == r.colLast) return a1;
    a1 += L":$" + c2 + L"$" + std::to_wstring(r.rwLast + 1);
    return a1;
}

// Resolve an IDSHEET to its workbook and worksheet names by building an
// xltypeRef carrying that idSheet and asking the C API's xlSheetNm for the full
// "[Book1]Sheet1" name (legal from the deferred-runner command context). Splits
// it into book="Book1" and sheet="Sheet1". Returns false (and the cell self-
// heals next recalc) on any failure: idSheet==0, xlSheetNm failure, a name with
// no "[...]" book prefix, or empty parts. Never throws.
bool ResolveSheetFullName(IDSHEET idSheet, std::wstring& book, std::wstring& sheet) {
    if (idSheet == 0) return false;

    // A single-cell rect at (0,0) is enough for xlSheetNm: it reads only
    // val.mref.idSheet. `mref` must outlive the call (stack local here). This is
    // a borrowed input operand, NOT an Excel result, so it must NOT go into a
    // ScopedXLOPER12Result (whose destructor would xlFree a ref we own).
    XLMREF12 mref{};
    mref.count = 1;
    mref.reftbl[0].rwFirst = mref.reftbl[0].rwLast = 0;
    mref.reftbl[0].colFirst = mref.reftbl[0].colLast = 0;
    XLOPER12 ref{};
    ref.xltype = xltypeRef;
    ref.val.mref.idSheet = idSheet;
    ref.val.mref.lpmref = &mref;

    ScopedXLOPER12Result xName;
    if (xll::CallExcel(xlSheetNm, xName, &ref) != xlretSuccess) return false;
    const DWORD nt = xName.get()->xltype & ~(xlbitDLLFree | xlbitXLFree);
    if ((nt & xltypeStr) == 0 || !xName.get()->val.str) return false;

    // Excel hands back a Pascal-style wide string, e.g. "[Book1]Sheet1".
    const std::wstring full = PascalToWString(xName.get()->val.str);
    const size_t lb = full.find(L'[');
    const size_t rb = full.find(L']');
    if (lb == std::wstring::npos || rb == std::wstring::npos || rb <= lb + 1)
        return false;
    book = full.substr(lb + 1, rb - lb - 1);
    sheet = full.substr(rb + 1);
    if (book.empty() || sheet.empty()) return false;
    return true;
}

// Late-bound default-member lookup: Workbooks(name) / Worksheets(name) resolve
// via the collection's `Item` member. Returns the child IDispatch (AddRef'd;
// caller Releases) or nullptr. `name` is copied into a transient BSTR that this
// helper owns and frees — xll::com::Invoke only BORROWS the arg VARIANT, so the
// caller (here) retains ownership of the BSTR. Never throws.
IDispatch* CollectionItem(IDispatch* coll, const std::wstring& name) {
    if (!coll) return nullptr;
    BSTR bname = SysAllocString(name.c_str());
    if (!bname) return nullptr;

    VARIANT arg; VariantInit(&arg);
    arg.vt = VT_BSTR;
    arg.bstrVal = bname;

    IDispatch* child = nullptr;
    VARIANT res; VariantInit(&res);
    // Item is a propget that also accepts the call form; pass both flags so the
    // automation server picks the right binding (matches the COM `Coll(name)`
    // default-member idiom).
    if (SUCCEEDED(xll::com::Invoke(coll, L"Item",
                                   DISPATCH_PROPERTYGET | DISPATCH_METHOD,
                                   { arg }, &res)) &&
        res.vt == VT_DISPATCH && res.pdispVal) {
        child = res.pdispVal;
        child->AddRef();
    }
    VariantClear(&res);
    SysFreeString(bname); // we own it; Invoke only borrowed it
    return child;
}

// Resolve idSheet -> the COM Worksheet IDispatch via
// Workbooks("Book").Worksheets("Sheet"). Returns AddRef'd (caller Releases) or
// nullptr on any failure (unresolvable sheet name, missing workbook/sheet).
// Every intermediate IDispatch is released on every path. Never throws.
IDispatch* ResolveWorksheet(IDispatch* pWorkbooks, IDSHEET idSheet) {
    std::wstring book, sheet;
    if (!ResolveSheetFullName(idSheet, book, sheet)) return nullptr;

    IDispatch* pWorkbook = CollectionItem(pWorkbooks, book);
    if (!pWorkbook) return nullptr;

    IDispatch* pWorksheets = nullptr;
    {
        VARIANT v; VariantInit(&v);
        if (SUCCEEDED(xll::com::GetProperty(pWorkbook, L"Worksheets", &v)) &&
            v.vt == VT_DISPATCH && v.pdispVal) {
            pWorksheets = v.pdispVal;
            pWorksheets->AddRef();
        }
        VariantClear(&v);
    }
    pWorkbook->Release();
    if (!pWorksheets) return nullptr;

    IDispatch* pSheet = CollectionItem(pWorksheets, sheet);
    pWorksheets->Release();
    return pSheet;
}

// Apply a number-format string to a worksheet range WITHOUT selecting it:
//   pSheet->Range(a1)->NumberFormat = format
// Returns true only when the property PUT succeeds. Releases the transient Range
// IDispatch and frees both transient BSTRs on every path. Never throws.
bool ApplyRangeNumberFormat(IDispatch* pSheet, const std::wstring& a1,
                            const std::wstring& format) {
    if (!pSheet) return false;

    // pSheet->Range(a1)
    IDispatch* pRange = nullptr;
    {
        BSTR ba1 = SysAllocString(a1.c_str());
        if (!ba1) return false;
        VARIANT arg; VariantInit(&arg);
        arg.vt = VT_BSTR;
        arg.bstrVal = ba1;
        VARIANT res; VariantInit(&res);
        if (SUCCEEDED(xll::com::Invoke(pSheet, L"Range",
                                       DISPATCH_PROPERTYGET | DISPATCH_METHOD,
                                       { arg }, &res)) &&
            res.vt == VT_DISPATCH && res.pdispVal) {
            pRange = res.pdispVal;
            pRange->AddRef();
        }
        VariantClear(&res);
        SysFreeString(ba1);
    }
    if (!pRange) return false;

    // pRange->NumberFormat = format
    bool ok = false;
    BSTR bfmt = SysAllocString(format.c_str());
    if (bfmt) {
        VARIANT arg; VariantInit(&arg);
        arg.vt = VT_BSTR;
        arg.bstrVal = bfmt;
        ok = SUCCEEDED(xll::com::Invoke(pRange, L"NumberFormat",
                                        DISPATCH_PROPERTYPUT, { arg }, nullptr));
        SysFreeString(bfmt);
    }
    pRange->Release();
    return ok;
}

} // namespace

void DrainAndApplyDateFormats() {
    auto& inst = PendingDateFormats::Instance();
    auto items = inst.Drain();
    if (items.empty()) return;

    // Acquire the in-process Application IDispatch ONCE for the whole drain (not
    // per rectangle). On failure (no workbook window yet, native object model
    // unreachable) mark nothing: every cell self-heals on the next recalc — the
    // same failure semantics as before. Released at the end of the drain.
    IDispatch* pApp = xll::com::AcquireExcelApplication();
    if (!pApp) return;

    IDispatch* pWorkbooks = nullptr;
    {
        VARIANT v; VariantInit(&v);
        if (SUCCEEDED(xll::com::GetProperty(pApp, L"Workbooks", &v)) &&
            v.vt == VT_DISPATCH && v.pdispVal) {
            pWorkbooks = v.pdispVal;
            pWorkbooks->AddRef();
        }
        VariantClear(&v);
    }
    if (!pWorkbooks) { pApp->Release(); return; }

    // GROUP by (idSheet, format). Cells with different sheets or different
    // format strings must NEVER be merged into one rectangle: a rect carries a
    // single format and targets a single sheet. Within a group the cells are
    // then greedy-meshed into rectangular blocks so contiguous same-format date
    // cells (the common YDH case: one ~21-cell date COLUMN, identical format)
    // collapse to ONE COM Range.NumberFormat assignment instead of one per cell.
    struct GroupKey {
        IDSHEET idSheet;
        std::wstring format;
        bool operator<(const GroupKey& o) const {
            if (idSheet != o.idSheet) return idSheet < o.idSheet;
            return format < o.format;
        }
    };
    std::map<GroupKey, std::vector<mesh::MeshCell>> groups;

    for (const auto& pf : items) {
        const int row = pf.ref.rwFirst;
        const int col = pf.ref.colFirst;
        // Once-per-cell: a cell already in the formatted set costs ZERO COM
        // work — this is the hot path on every keystroke recalc. (The producer
        // already filters, but a cell could be enqueued twice within one cycle,
        // or marked by a prior drain, so re-check here too.) Dropping it BEFORE
        // meshing means already-formatted cells become holes, so a rectangle
        // never spans them.
        if (inst.AlreadyFormatted(pf.idSheet, row, col)) continue;
        GroupKey key{pf.idSheet, pf.format};
        groups[key].push_back(mesh::MeshCell{row, col});
    }

    // Cache the resolved Worksheet IDispatch per idSheet so a multi-rectangle /
    // multi-format sheet resolves the COM chain once, not per rectangle.
    std::map<IDSHEET, IDispatch*> sheetCache;

    for (auto& kv : groups) {
        const IDSHEET idSheet = kv.first.idSheet;
        const std::wstring& format = kv.first.format;

        // Resolve (and cache) the target Worksheet IDispatch for this idSheet.
        IDispatch* pSheet = nullptr;
        auto cached = sheetCache.find(idSheet);
        if (cached != sheetCache.end()) {
            pSheet = cached->second; // may be nullptr (resolution failed once)
        } else {
            pSheet = ResolveWorksheet(pWorkbooks, idSheet);
            sheetCache[idSheet] = pSheet; // cache nullptr too, so we don't retry
        }
        if (!pSheet) continue; // unresolved sheet: leave cells unmarked (self-heal)

        const std::vector<mesh::MeshRect> rects = mesh::GreedyMesh(kv.second);

        for (const auto& rect : rects) {
            try {
                XLREF12 ref12;
                ref12.rwFirst  = rect.rowFirst;
                ref12.rwLast   = rect.rowLast;
                ref12.colFirst = rect.colFirst;
                ref12.colLast  = rect.colLast;

                // First touch: apply the auto-format UNCONDITIONALLY (no
                // xlfGetCell read, no IsDateLikeFormat check) via COM
                // Worksheet.Range(a1).NumberFormat = <format>. Value-driven
                // formatting wins: this overrides any pre-existing user format
                // exactly once. CRUCIALLY this does NOT use xlcSelect /
                // xlcFormatNumber — those operate on the ACTIVE selection and
                // would steal focus / make the screen flicker. The COM Range
                // route targets the cells directly, leaving the user's active
                // cell and selection untouched. On success mark EVERY cell in
                // the rectangle so all later recalcs skip them; on failure mark
                // NONE, so the whole rect is retried next cycle.
                if (ApplyRangeNumberFormat(pSheet, BuildA1(ref12), format)) {
                    for (int r = rect.rowFirst; r <= rect.rowLast; ++r) {
                        for (int c = rect.colFirst; c <= rect.colLast; ++c) {
                            inst.MarkFormatted(idSheet, r, c);
                        }
                    }
                }
            } catch (...) { /* skip this rect; never break calc-end */ }
        }
    }

    for (auto& sc : sheetCache) {
        if (sc.second) sc.second->Release();
    }
    pWorkbooks->Release();
    pApp->Release();
}

} // namespace xll
