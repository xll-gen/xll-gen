// Offline unit test for the `grid` ARGUMENT conversion path:
//   * xll::ConvertGridArg / xll::MeasureRefArg  (internal/assets/files/src/xll_cache.cpp)
//   * SHMAllocator                              (internal/assets/files/include/SHMAllocator.h)
//
// Both are reachable without Excel: ConvertGridArg touches Excel only through
// xlCoerce/xlFree, which this harness supplies, and SHMAllocator is a plain
// flatbuffers::Allocator. Driven by internal/assets/gridarg_cpp_test.go (which
// compiles it with g++ and checks the exit code). Prints one line per case;
// exit code = number of failures.
//
// ---------------------------------------------------------------------------
// WHAT IT PINS  (all three reproduced against the shipped showcase build on
// Excel 16.0.20131.20154, cache disabled — see AGENTS.md §19.5)
// ---------------------------------------------------------------------------
//
//  1. HIGH, process death. =SumGrid($P:$R) killed Excel. The mechanism is NOT
//     "3.1M cells is too much for xlCoerce": the measured cliff is at ~19,600
//     cells (120x120 returned the right answer, 140x140 died), which is exactly
//     where protocol::Grid's ~28 bytes per cell stops fitting the 512 KiB SHM
//     request buffer the builder writes into. SHMAllocator::allocate returned
//     nullptr there, and flatbuffers' Allocator::reallocate_downward memcpy's
//     into whatever allocate() hands back — an access violation inside the CRT
//     memcpy, in the Excel process. TestAllocatorOverflowDoesNotCrash is the
//     regression: on the parent commit it CRASHES the harness.
//     The cell bound (kMaxGridArgCells) is the cheap pre-filter on top of that:
//     it stops the pathological input before Excel is asked to materialize
//     ~100 MB of XLOPER12 that will be thrown away.
//
//  2. MED, silent wrong answer. =SumGrid(($P$1:$R$5,$P$6:$R$10)) returned 0
//     while the contiguous $P$1:$R$10 returned 2805. xlCoerce cannot flatten a
//     union reference; it answers with an error VALUE, and the old code fed
//     that straight to ConvertGrid, whose non-multi fall-through wraps it as a
//     1x1 grid. The handler summed that to 0 and nothing anywhere reported a
//     problem. Two independent guards now refuse it — a structural multi-area
//     check before Excel is asked, and a "did xlCoerce actually answer with the
//     xltypeMulti we requested?" check after.

#include "xll_cache.h"
#include "SHMAllocator.h"
#include "types/utility.h"

#include <atomic>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

// ---------------------------------------------------------------------------
// Offline Excel stub
// ---------------------------------------------------------------------------

// g_hModule is declared extern by types/utility.h.
HINSTANCE g_hModule = nullptr;

// What the next xlCoerce should yield; nullptr => the call FAILS (xlretUncalced).
static const XLOPER12* g_coerceResult = nullptr;
static std::atomic<int> g_coerceCalls{0};

int pascal Excel12v(int xlfn, LPXLOPER12 operRes, int count, LPXLOPER12 opers[]) {
    (void)count;
    (void)opers;
    if (xlfn == xlCoerce) {
        g_coerceCalls.fetch_add(1);
        if (!operRes) return xlretFailed;
        if (!g_coerceResult) return xlretUncalced;
        *operRes = *g_coerceResult; // shallow: the test owns the storage
        return xlretSuccess;
    }
    if (xlfn == xlFree) return xlretSuccess;
    return xlretFailed;
}

int _cdecl Excel12(int xlfn, LPXLOPER12 operRes, int count, ...) {
    (void)xlfn;
    (void)operRes;
    (void)count;
    return xlretFailed;
}

// ---------------------------------------------------------------------------
// Test plumbing
// ---------------------------------------------------------------------------

static int g_failures = 0;
static int g_checks = 0;

static void Check(bool ok, const std::string& what) {
    ++g_checks;
    if (!ok) {
        ++g_failures;
        std::printf("FAIL  %s\n", what.c_str());
    } else {
        std::printf("ok    %s\n", what.c_str());
    }
}

static XLOPER12 Num(double d) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeNum;
    x.val.num = d;
    return x;
}

static XLOPER12 Err(int e) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeErr;
    x.val.err = e;
    return x;
}

static XLOPER12 Multi(int rows, int cols, XLOPER12* cells) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeMulti;
    x.val.array.rows = rows;
    x.val.array.columns = cols;
    x.val.array.lparray = cells;
    return x;
}

static XLOPER12 SRef(int rwFirst, int rwLast, int colFirst, int colLast) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeSRef;
    x.val.sref.count = 1;
    x.val.sref.ref.rwFirst = rwFirst;
    x.val.sref.ref.rwLast = rwLast;
    x.val.sref.ref.colFirst = colFirst;
    x.val.sref.ref.colLast = colLast;
    return x;
}

static XLREF12 Rect(int rwF, int rwL, int cF, int cL) {
    XLREF12 r;
    r.rwFirst = rwF;
    r.rwLast = rwL;
    r.colFirst = cF;
    r.colLast = cL;
    return r;
}

struct RefHolder {
    std::vector<char> mrefBuf;
    XLOPER12 op;
    RefHolder(IDSHEET sheet, const std::vector<XLREF12>& rects) {
        mrefBuf.resize(sizeof(XLMREF12) + rects.size() * sizeof(XLREF12), 0);
        XLMREF12* m = reinterpret_cast<XLMREF12*>(mrefBuf.data());
        m->count = (WORD)rects.size();
        for (size_t i = 0; i < rects.size(); ++i) m->reftbl[i] = rects[i];
        std::memset(&op, 0, sizeof(op));
        op.xltype = xltypeRef;
        op.val.mref.idSheet = sheet;
        op.val.mref.lpmref = m;
    }
};

// Runs ConvertGridArg into a throwaway builder and reports the status plus the
// geometry of the grid that was actually produced.
struct GridOutcome {
    xll::GridArgStatus status;
    uint32_t rows;
    uint32_t cols;
    uint32_t cells;   // length of the data vector
    int coerceCalls;  // xlCoerce invocations this conversion made
};

static GridOutcome Convert(const XLOPER12* op, uint64_t maxCells = xll::kMaxGridArgCells) {
    GridOutcome out{xll::GridArgStatus::kOk, 0, 0, 0, 0};
    const int before = g_coerceCalls.load();

    flatbuffers::FlatBufferBuilder fbb(1024);
    auto off = xll::ConvertGridArg(op, fbb, &out.status, maxCells);
    fbb.Finish(off);

    const protocol::Grid* g = flatbuffers::GetRoot<protocol::Grid>(fbb.GetBufferPointer());
    out.rows = g->rows();
    out.cols = g->cols();
    out.cells = g->data() ? (uint32_t)g->data()->size() : 0u;
    out.coerceCalls = g_coerceCalls.load() - before;
    return out;
}

// ---------------------------------------------------------------------------
// 1. MeasureRefArg — the shape measurement the guards are built on
// ---------------------------------------------------------------------------

static void TestMeasureRefArg() {
    std::printf("\n-- MeasureRefArg --\n");

    uint64_t cells = 123;
    uint32_t areas = 123;

    // A same-sheet range arrives as xltypeSRef: ONE rect, no sheet id.
    XLOPER12 sref = SRef(0, 9, 15, 17); // P1:R10 -> 10 rows x 3 cols
    xll::MeasureRefArg(&sref, &cells, &areas);
    Check(cells == 30 && areas == 1, "SRef P1:R10 measures 30 cells / 1 area");

    // A whole-column reference is the same shape, just huge. This is the input
    // that killed Excel: 1,048,576 rows x 3 columns.
    XLOPER12 wholeCols = SRef(0, 1048575, 15, 17);
    xll::MeasureRefArg(&wholeCols, &cells, &areas);
    Check(cells == 3145728ull && areas == 1, "SRef $P:$R measures 3,145,728 cells / 1 area");

    // A union reference arrives as xltypeRef with SEVERAL rects.
    RefHolder twoAreas(1, {Rect(0, 4, 15, 17), Rect(5, 9, 15, 17)});
    xll::MeasureRefArg(&twoAreas.op, &cells, &areas);
    Check(cells == 30 && areas == 2, "xltypeRef union (P1:R5,P6:R10) measures 30 cells / 2 areas");

    RefHolder oneArea(1, {Rect(0, 9, 15, 17)});
    xll::MeasureRefArg(&oneArea.op, &cells, &areas);
    Check(cells == 30 && areas == 1, "xltypeRef single area measures 30 cells / 1 area");

    // Non-references and degenerate shapes report "not a reference".
    XLOPER12 n = Num(1.0);
    xll::MeasureRefArg(&n, &cells, &areas);
    Check(cells == 0 && areas == 0, "a plain number is not a reference (0 areas)");

    xll::MeasureRefArg(nullptr, &cells, &areas);
    Check(cells == 0 && areas == 0, "nullptr is not a reference (0 areas)");

    // An inverted rect must contribute nothing rather than a huge unsigned wrap.
    XLOPER12 inverted = SRef(9, 0, 17, 15);
    xll::MeasureRefArg(&inverted, &cells, &areas);
    Check(cells == 0 && areas == 1, "an inverted rect measures 0 cells (no unsigned wrap)");
}

// ---------------------------------------------------------------------------
// 2. The happy path must be untouched
// ---------------------------------------------------------------------------

static void TestContiguousReferenceStillConverts() {
    std::printf("\n-- contiguous reference (the control case) --\n");

    // 2x3 of numbers, exactly what xlCoerce hands back for $P$1:$R$2.
    XLOPER12 cells[6];
    for (int i = 0; i < 6; ++i) cells[i] = Num(i + 1);
    XLOPER12 coerced = Multi(2, 3, cells);

    g_coerceResult = &coerced;
    XLOPER12 sref = SRef(0, 1, 15, 17);
    GridOutcome out = Convert(&sref);
    g_coerceResult = nullptr;

    Check(out.status == xll::GridArgStatus::kOk, "contiguous SRef converts (kOk)");
    Check(out.coerceCalls == 1, "contiguous SRef is coerced exactly once");
    Check(out.rows == 2 && out.cols == 3 && out.cells == 6,
          "contiguous SRef yields the 2x3 grid of coerced VALUES");

    // An array LITERAL ({1,2;3,4}) already arrives as xltypeMulti and must pass
    // through without asking Excel anything.
    GridOutcome lit = Convert(&coerced);
    Check(lit.status == xll::GridArgStatus::kOk, "xltypeMulti literal converts (kOk)");
    Check(lit.coerceCalls == 0, "xltypeMulti literal is not coerced");
    Check(lit.rows == 2 && lit.cols == 3 && lit.cells == 6,
          "xltypeMulti literal keeps its geometry");
}

// ---------------------------------------------------------------------------
// 3. HIGH — an over-large reference is refused BEFORE Excel is asked
// ---------------------------------------------------------------------------

static void TestOversizedReferenceRefused() {
    std::printf("\n-- oversized reference --\n");

    // The reported input: three whole columns. On the parent commit this was
    // coerced (~100 MB of XLOPER12) and then serialized into a 512 KiB arena.
    XLOPER12 wholeCols = SRef(0, 1048575, 15, 17);
    GridOutcome out = Convert(&wholeCols);
    Check(out.status == xll::GridArgStatus::kTooLarge,
          "$P:$R (3.1M cells) is refused as kTooLarge");
    Check(out.coerceCalls == 0,
          "$P:$R is refused WITHOUT calling xlCoerce (no 100 MB materialization)");
    Check(out.rows == 0 && out.cols == 0 && out.cells == 0,
          "a refused reference yields an EMPTY grid, never a partial one");

    // One whole column is over the bound too.
    XLOPER12 oneCol = SRef(0, 1048575, 15, 15);
    Check(Convert(&oneCol).status == xll::GridArgStatus::kTooLarge,
          "$P:$P (1,048,576 cells) is refused as kTooLarge");

    // The bound applies to the multi-rect shape as well.
    RefHolder bigRef(1, {Rect(0, 1048575, 15, 15)});
    Check(Convert(&bigRef.op).status == xll::GridArgStatus::kTooLarge,
          "an xltypeRef whole column is refused as kTooLarge");

    // Boundary: the limit itself is accepted, one cell more is not. Driven
    // through the maxCells parameter so the case does not need a 16k fixture.
    XLOPER12 cell = Num(1.0);
    XLOPER12 oneByOne = Multi(1, 1, &cell);
    g_coerceResult = &oneByOne;
    XLOPER12 exact = SRef(0, 3, 0, 2);   // 4 x 3 = 12 cells
    XLOPER12 over = SRef(0, 3, 0, 3);    // 4 x 4 = 16 cells
    Check(Convert(&exact, 12).status == xll::GridArgStatus::kOk,
          "a reference of exactly maxCells is accepted");
    Check(Convert(&over, 12).status == xll::GridArgStatus::kTooLarge,
          "a reference of maxCells+4 is refused");
    g_coerceResult = nullptr;

    // An inline value array reaches the same fixed-size arena, so it is bounded
    // by the same rule.
    XLOPER12 litCells[16];
    for (int i = 0; i < 16; ++i) litCells[i] = Num(i);
    XLOPER12 bigLiteral = Multi(4, 4, litCells);
    Check(Convert(&bigLiteral, 12).status == xll::GridArgStatus::kTooLarge,
          "an xltypeMulti literal over maxCells is refused too");

    // The production default is the documented 16384.
    Check(xll::kMaxGridArgCells == 16384,
          "kMaxGridArgCells is 16384 (below the ~18.7k cells a 512 KiB slot holds)");
}

// ---------------------------------------------------------------------------
// 4. MED — a multi-area (union) reference is refused, not silently wrong
// ---------------------------------------------------------------------------

static void TestMultiAreaReferenceRefused() {
    std::printf("\n-- multi-area (union) reference --\n");

    // =SumGrid(($P$1:$R$5,$P$6:$R$10)) — 2 areas, 30 cells, well under any size
    // bound, so ONLY the union check can catch it.
    RefHolder unionRef(1, {Rect(0, 4, 15, 17), Rect(5, 9, 15, 17)});

    // Point the stub at a perfectly good 5x3 grid: if the union check did not
    // exist, the conversion would happily succeed and ship the FIRST area's
    // values as if they were the whole argument.
    XLOPER12 cells[15];
    for (int i = 0; i < 15; ++i) cells[i] = Num(i + 1);
    XLOPER12 firstArea = Multi(5, 3, cells);
    g_coerceResult = &firstArea;

    GridOutcome out = Convert(&unionRef.op);
    g_coerceResult = nullptr;

    Check(out.status == xll::GridArgStatus::kMultiArea,
          "a 2-area union reference is refused as kMultiArea");
    Check(out.coerceCalls == 0,
          "the union is refused STRUCTURALLY, without asking xlCoerce "
          "(the diagnosis must not depend on how Excel chooses to fail)");
    Check(out.rows == 0 && out.cols == 0 && out.cells == 0,
          "a refused union yields an EMPTY grid, not the first area's values");

    // Second, independent guard: xlCoerce reporting SUCCESS with something that
    // is not the xltypeMulti we asked for. This is what real Excel does for a
    // union reference (it answers #VALUE!), and feeding it to ConvertGrid is
    // what produced the reported 0.
    XLOPER12 coerceError = Err(xlerrValue);
    g_coerceResult = &coerceError;
    XLOPER12 sref = SRef(0, 9, 15, 17);
    GridOutcome nonArray = Convert(&sref);
    g_coerceResult = nullptr;

    Check(nonArray.status == xll::GridArgStatus::kNotAnArray,
          "xlCoerce answering a non-xltypeMulti is refused as kNotAnArray");
    Check(!(nonArray.rows == 1 && nonArray.cols == 1 && nonArray.cells == 1),
          "a coerce error must NOT become a 1x1 grid holding the error "
          "(that is the silent wrong answer: the handler sums it to 0)");
}

// ---------------------------------------------------------------------------
// 5. The other refusal paths
// ---------------------------------------------------------------------------

static void TestCoerceFailureAndNull() {
    std::printf("\n-- coerce failure / null argument --\n");

    g_coerceResult = nullptr; // stub answers xlretUncalced
    XLOPER12 sref = SRef(0, 9, 15, 17);
    GridOutcome out = Convert(&sref);
    Check(out.status == xll::GridArgStatus::kCoerceFailed,
          "a failed xlCoerce is reported as kCoerceFailed");
    Check(out.coerceCalls == 1, "the failed coerce was actually attempted");
    Check(out.rows == 0 && out.cols == 0, "a failed coerce yields an EMPTY grid");

    // types' ConvertGrid dereferences its argument unconditionally, so a null
    // must never reach it.
    GridOutcome nul = Convert(nullptr);
    Check(nul.status == xll::GridArgStatus::kNotAnArray,
          "a null argument is refused instead of dereferenced");

    // Every status has a non-empty label so the wrapper's log line is useful.
    const xll::GridArgStatus all[] = {
        xll::GridArgStatus::kOk,        xll::GridArgStatus::kCoerceFailed,
        xll::GridArgStatus::kNotAnArray, xll::GridArgStatus::kMultiArea,
        xll::GridArgStatus::kTooLarge,
    };
    bool labelled = true;
    for (auto s : all) {
        const char* t = xll::GridArgStatusText(s);
        if (!t || !*t) labelled = false;
    }
    Check(labelled, "every GridArgStatus renders a non-empty label");
}

// ---------------------------------------------------------------------------
// 6. HIGH — SHMAllocator must never hand flatbuffers a null buffer
// ---------------------------------------------------------------------------

static void TestAllocatorOverflowDoesNotCrash() {
    std::printf("\n-- SHMAllocator overflow --\n");

    // A payload that fits leaves the allocator untouched and stays in the
    // "shared memory" region.
    {
        std::vector<uint8_t> arena(4096, 0);
        SHMAllocator alloc(arena.data(), arena.size());
        flatbuffers::FlatBufferBuilder fbb(arena.size(), &alloc, false);
        auto v = fbb.CreateVector(std::vector<uint8_t>(64, 7));
        fbb.Finish(protocol::CreateGrid(fbb, 1, 1, 0));
        (void)v;
        Check(!alloc.Overflowed(), "a fitting payload does not latch Overflowed()");
        Check(fbb.GetBufferPointer() >= arena.data() &&
                  fbb.GetBufferPointer() < arena.data() + arena.size(),
              "a fitting payload is built inside the shared-memory arena");
    }

    // THE REGRESSION. On the parent commit allocate() returned nullptr here and
    // flatbuffers' Allocator::reallocate_downward memcpy'd into it — this block
    // took the whole process down with an access violation, which inside Excel
    // is exactly what =SumGrid($P:$R) did.
    {
        std::vector<uint8_t> arena(4096, 0);
        SHMAllocator alloc(arena.data(), arena.size());
        flatbuffers::FlatBufferBuilder fbb(arena.size(), &alloc, false);
        auto v = fbb.CreateVector(std::vector<uint8_t>(256 * 1024, 3));
        fbb.Finish(protocol::CreateGrid(fbb, 1, 1, 0));
        (void)v;
        Check(alloc.Overflowed(),
              "a payload larger than the arena latches Overflowed() instead of "
              "returning nullptr into flatbuffers' memcpy");
        Check(fbb.GetSize() > arena.size(),
              "the over-capacity payload really did exceed the arena");
        Check(!(fbb.GetBufferPointer() >= arena.data() &&
                fbb.GetBufferPointer() < arena.data() + arena.size()),
              "the over-capacity payload is NOT in the shared-memory arena "
              "(so the caller must refuse to send it)");
    }

    // An INVALID slot answers GetReqBuffer()==nullptr / GetMaxReqSize()==0. The
    // builder must still be usable rather than crashing on the first byte.
    {
        SHMAllocator alloc(nullptr, 0);
        flatbuffers::FlatBufferBuilder fbb(0, &alloc, false);
        fbb.Finish(protocol::CreateGrid(fbb, 1, 1, 0));
        Check(alloc.Overflowed(), "a zero-size arena (invalid slot) latches Overflowed()");
        Check(fbb.GetSize() > 0, "a zero-size arena still produces a well-formed buffer");
    }

    // Repeated growth must keep the latch set and must not double-free the
    // shared-memory buffer (a bad deallocate() shows up as a heap abort here).
    {
        std::vector<uint8_t> arena(1024, 0);
        SHMAllocator alloc(arena.data(), arena.size());
        flatbuffers::FlatBufferBuilder fbb(arena.size(), &alloc, false);
        for (int i = 0; i < 8; ++i) {
            fbb.CreateVector(std::vector<uint8_t>(8 * 1024, (uint8_t)i));
        }
        fbb.Finish(protocol::CreateGrid(fbb, 1, 1, 0));
        Check(alloc.Overflowed(), "the overflow latch survives repeated growth");
    }
}

// ---------------------------------------------------------------------------

int main() {
    std::setvbuf(stdout, nullptr, _IONBF, 0);

    TestMeasureRefArg();
    TestContiguousReferenceStillConverts();
    TestOversizedReferenceRefused();
    TestMultiAreaReferenceRefused();
    TestCoerceFailureAndNull();
    TestAllocatorOverflowDoesNotCrash();

    std::printf("\n%d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
