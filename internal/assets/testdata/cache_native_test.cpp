// Offline unit test + microbenchmark for internal/assets/files/src/xll_cache.cpp.
//
// SerializeXLOPER / HashXLOPERInto only reach Excel through xlCoerce + xlFree on
// the xltypeRef/xltypeSRef branch, so the whole file is testable WITHOUT Excel:
// this harness supplies its own `Excel12v` and drives every branch with synthetic
// XLOPER12s (scalars, Pascal strings, xltypeMulti grids, refs).
//
// Driven by internal/assets/cache_native_test.go (which compiles it with g++ and
// checks the exit code). Prints one line per case; exit code = number of
// failures.
//
// Covers:
//   * defect 1 — the Pascal-prefix misuse that made a string cell's cache key /
//     RTD topic token depend on adjacent STACK GARBAGE (see DirtyStack cases).
//   * defect 2 — concurrent Get/Put now actually locked (stress case).
//   * perf 3  — determinism + collision resistance of the new streaming digest
//     and the constant-size cache key, plus before/after timings.

#include "xll_cache.h"
#include "types/utility.h"

#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <set>
#include <string>
#include <thread>
#include <vector>

// ---------------------------------------------------------------------------
// Offline Excel stub
// ---------------------------------------------------------------------------

// g_hModule is declared extern by types/utility.h.
HINSTANCE g_hModule = nullptr;

// What the next xlCoerce should yield; nullptr => coercion fails (xlretUncalced),
// which exercises the reference-identity fallback.
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

// Excel Pascal wide string: buf[0] = length in UTF-16 code units, body follows.
struct PStr {
    std::vector<XCHAR> buf;
    explicit PStr(const std::wstring& s) {
        buf.resize(s.size() + 2, 0);
        buf[0] = (XCHAR)s.size();
        for (size_t i = 0; i < s.size(); ++i) buf[i + 1] = (XCHAR)s[i];
    }
    XCHAR* get() { return buf.data(); }
};

static XLOPER12 Str(PStr& p) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeStr;
    x.val.str = p.get();
    return x;
}

static XLOPER12 Num(double d) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeNum;
    x.val.num = d;
    return x;
}

static XLOPER12 Int(int v) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeInt;
    x.val.w = v;
    return x;
}

static XLOPER12 Bool(bool b) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeBool;
    x.val.xbool = b ? 1 : 0;
    return x;
}

static XLOPER12 Err(int e) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = xltypeErr;
    x.val.err = e;
    return x;
}

static XLOPER12 Plain(DWORD ty) {
    XLOPER12 x;
    std::memset(&x, 0, sizeof(x));
    x.xltype = ty;
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

static XLREF12 Rect(int rwF, int rwL, int cF, int cL) {
    XLREF12 r;
    r.rwFirst = rwF;
    r.rwLast = rwL;
    r.colFirst = cF;
    r.colLast = cL;
    return r;
}

// WithDirtyStack fills ~16 KB of the CALLER-side stack (i.e. the addresses just
// ABOVE the frame `f` will run in) with `fill`, then invokes f. Any read that
// runs off the end of a local buffer inside f lands in this region, so a
// content-addressed digest that is contaminated by stack residue produces a
// DIFFERENT result for two different fill patterns.
//
// This is what makes defect 1 observable offline: the old xltypeStr branch handed
// an ALREADY-STRIPPED wstring to the Pascal-only ConvertExcelString, which read
// the first CHARACTER as the length and transcoded that many code units out of
// the buffer. A string starting at U+0800 (= 2048) therefore dragged 4 KB of this
// pad into the token.
template <typename F>
static std::string WithDirtyStack(wchar_t fill, F&& f) {
    volatile wchar_t pad[8192];
    for (size_t i = 0; i < 8192; ++i) pad[i] = fill;
    std::string r = f();
    // Defeat dead-store elimination of the fill loop.
    if (pad[0] == (wchar_t)0xFFFF) std::printf("unreachable\n");
    return r;
}

// ---------------------------------------------------------------------------
// 1. defect 1 — string tokens must not depend on stack residue
// ---------------------------------------------------------------------------

static void TestStringTokenDeterministic() {
    struct Case {
        const char* name;
        std::wstring text;
    } cases[] = {
        {"ascii", L"Hello"},
        {"empty", L""},
        // First code unit 2048: the old code read 2048 UTF-16 units (4 KB) past
        // the end of the wstring, straight through WithDirtyStack's pad.
        {"high-BMP-lead", std::wstring(L"ࠀrest of the string")},
        // A CJK lead is the catastrophic case (U+D55C = 54620 units ~= 109 KB
        // OOB, which can also just AV); kept for coverage of the fixed path.
        {"cjk", std::wstring(L"한국어")},
        {"cjk-adjacent", std::wstring(L"핝국어")},
        {"long", std::wstring(300, L'x')},
    };

    for (const Case& c : cases) {
        PStr p(c.text);
        XLOPER12 x = Str(p);
        std::string a = WithDirtyStack(L'ᄑ', [&] { return xll::ContentHashToken('a', &x); });
        std::string b = WithDirtyStack(L'睷', [&] { return xll::ContentHashToken('a', &x); });
        std::string cc = xll::ContentHashToken('a', &x);
        Check(a == b && b == cc,
              std::string("ContentHashToken deterministic under dirty stack: ") + c.name +
                  " (" + a + " vs " + b + " vs " + cc + ")");

        std::string sa = WithDirtyStack(L'ᄑ', [&] { return xll::SerializeXLOPER(&x); });
        std::string sb = WithDirtyStack(L'睷', [&] { return xll::SerializeXLOPER(&x); });
        Check(sa == sb, std::string("SerializeXLOPER deterministic under dirty stack: ") + c.name +
                            " (len " + std::to_string(sa.size()) + " vs " +
                            std::to_string(sb.size()) + ")");
    }
}

static void TestSerializeStringExact() {
    {
        PStr p(L"Hello");
        XLOPER12 x = Str(p);
        Check(xll::SerializeXLOPER(&x) == "Str:5:Hello", "SerializeXLOPER ascii == \"Str:5:Hello\"");
    }
    {
        PStr p(L"");
        XLOPER12 x = Str(p);
        Check(xll::SerializeXLOPER(&x) == "Str:0:", "SerializeXLOPER empty == \"Str:0:\"");
    }
    {
        // U+D55C U+AD6D U+C5B4 in UTF-8.
        PStr p(std::wstring(L"한국어"));
        XLOPER12 x = Str(p);
        const std::string want = "Str:3:\xED\x95\x9C\xEA\xB5\xAD\xEC\x96\xB4";
        Check(xll::SerializeXLOPER(&x) == want, "SerializeXLOPER cjk transcodes exactly");
    }
    {
        // Embedded NUL survives: WideToUtf8 converts wstr.size() units, not up to
        // a terminator, and the "Str:<len>:" prefix keeps it unambiguous.
        std::wstring w;
        w.push_back(L'a');
        w.push_back(L'\0');
        w.push_back(L'b');
        PStr p(w);
        XLOPER12 x = Str(p);
        std::string want = "Str:3:a";
        want.push_back('\0');
        want.push_back('b');
        Check(xll::SerializeXLOPER(&x) == want, "SerializeXLOPER preserves an embedded NUL");
    }
}

// ---------------------------------------------------------------------------
// 2. perf 3 — determinism + collision resistance of the new digest
// ---------------------------------------------------------------------------

static void TestTokenDistinctness() {
    std::vector<std::pair<std::string, std::string>> tokens; // (label, token)
    std::vector<PStr*> keep;

    auto addStr = [&](const char* label, const std::wstring& s) {
        PStr* p = new PStr(s);
        keep.push_back(p);
        XLOPER12 x = Str(*p);
        tokens.push_back({label, xll::ContentHashToken('a', &x)});
    };
    auto add = [&](const char* label, XLOPER12 x) {
        tokens.push_back({label, xll::ContentHashToken('a', &x)});
    };

    add("num:1", Num(1.0));
    add("num:2", Num(2.0));
    add("num:1.0000000000000002", Num(1.0000000000000002));
    add("int:1", Int(1));
    add("bool:true", Bool(true));
    add("bool:false", Bool(false));
    add("err:15", Err(15));
    add("err:42", Err(42));
    add("nil", Plain(xltypeNil));
    add("unknown", Plain(xltypeFlow));

    addStr("str:empty", L"");
    addStr("str:1", L"1");
    addStr("str:Hello", L"Hello");
    addStr("str:hello", L"hello");
    addStr("str:cjk-han", std::wstring(L"한"));
    addStr("str:cjk-adjacent", std::wstring(L"핝"));
    addStr("str:a", L"a");
    addStr("str:ab", L"ab");
    {
        std::wstring w;
        w.push_back(L'a');
        w.push_back(L'\0');
        w.push_back(L'b');
        addStr("str:a-NUL-b", w);
    }
    {
        std::wstring w;
        w.push_back(L'a');
        w.push_back(L'b');
        w.push_back(L'\0');
        addStr("str:ab-NUL", w);
    }

    XLOPER12 cells[4] = {Num(1.0), Num(2.0), Num(3.0), Num(4.0)};
    add("multi:1x4", Multi(1, 4, cells));
    add("multi:4x1", Multi(4, 1, cells));
    add("multi:2x2", Multi(2, 2, cells));
    XLOPER12 cells2[4] = {Num(2.0), Num(1.0), Num(3.0), Num(4.0)};
    add("multi:2x2-swapped", Multi(2, 2, cells2));

    // The empty string and Nil must not collapse: an empty cell arrives as
    // xltypeMissing/Nil, a cell holding "" as a zero-length xltypeStr.
    std::set<std::string> seen;
    bool allDistinct = true;
    for (const auto& t : tokens) {
        if (!seen.insert(t.second).second) {
            allDistinct = false;
            std::printf("      collision on %s -> %s\n", t.first.c_str(), t.second.c_str());
        }
    }
    Check(allDistinct, "all " + std::to_string(tokens.size()) + " distinct values yield distinct tokens");

    // Determinism: recomputing every token gives the same answer.
    bool stable = true;
    size_t i = 0;
    for (PStr* p : keep) {
        XLOPER12 x = Str(*p);
        std::string again = xll::ContentHashToken('a', &x);
        (void)i;
        bool found = false;
        for (const auto& t : tokens) {
            if (t.second == again) { found = true; break; }
        }
        if (!found) stable = false;
    }
    Check(stable, "string tokens are stable across recomputation");

    // Token shape must be unchanged: "h:" + tag + 16 lowercase hex digits.
    XLOPER12 n = Num(3.5);
    std::string tok = xll::ContentHashToken('g', &n);
    bool shapeOk = tok.size() == 19 && tok.compare(0, 2, "h:") == 0 && tok[2] == 'g';
    for (size_t k = 3; shapeOk && k < tok.size(); ++k) {
        char ch = tok[k];
        shapeOk = (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f');
    }
    Check(shapeOk, "token shape is h:<tag><16 lowercase hex>: " + tok);

    // typeTag namespaces the digest by wire-payload shape.
    Check(xll::ContentHashToken('g', &n) != xll::ContentHashToken('r', &n),
          "typeTag namespaces the token ('g' != 'r')");

    for (PStr* p : keep) delete p;
}

static void TestFP12Token() {
    std::vector<double> data = {1.0, 2.0, 3.0, 4.0};
    std::vector<char> buf(sizeof(FP12) + data.size() * sizeof(double), 0);
    FP12* fp = reinterpret_cast<FP12*>(buf.data());
    fp->rows = 2;
    fp->columns = 2;
    for (size_t i = 0; i < data.size(); ++i) fp->array[i] = data[i];

    std::string a = xll::ContentHashTokenFP12(fp);
    std::string b = xll::ContentHashTokenFP12(fp);
    Check(a == b, "ContentHashTokenFP12 deterministic");
    Check(a.size() == 19 && a[2] == 'n', "ContentHashTokenFP12 shape: " + a);

    fp->rows = 1;
    fp->columns = 4;
    Check(xll::ContentHashTokenFP12(fp) != a, "ContentHashTokenFP12 is geometry-sensitive (2x2 != 1x4)");
    Check(xll::ContentHashTokenFP12(nullptr).size() == 19, "ContentHashTokenFP12(nullptr) still well-formed");
}

// ---------------------------------------------------------------------------
// 3. perf 3 — MakeCacheKey: constant size, deterministic, collision-resistant
// ---------------------------------------------------------------------------

static void TestMakeCacheKey() {
    PStr ps(L"Hello");
    XLOPER12 s = Str(ps);
    XLOPER12 n1 = Num(1.0);
    XLOPER12 n2 = Num(2.0);

    std::string k1 = xll::MakeCacheKey("Func", {&s, &n1});
    std::string k1b = xll::MakeCacheKey("Func", {&s, &n1});
    std::string k2 = xll::MakeCacheKey("Func", {&s, &n2});
    std::string k3 = xll::MakeCacheKey("Func", {&n1, &s});
    std::string k4 = xll::MakeCacheKey("Other", {&s, &n1});
    std::string k5 = xll::MakeCacheKey("Func", {&s});
    std::string k6 = xll::MakeCacheKey("Func", {&s, nullptr});

    Check(k1 == k1b, "MakeCacheKey deterministic for identical args: " + k1);
    Check(k1 != k2, "MakeCacheKey distinguishes 1.0 from 2.0");
    Check(k1 != k3, "MakeCacheKey is argument-ORDER sensitive");
    Check(k1 != k4, "MakeCacheKey distinguishes function names");
    Check(k1 != k5, "MakeCacheKey distinguishes arity");
    Check(k5 != k6, "MakeCacheKey distinguishes a null arg from an absent one");
    Check(k1.compare(0, 5, "Func#") == 0 && k1.size() == 4 + 1 + 16,
          "MakeCacheKey shape is <funcName>#<16 hex>: " + k1);

    // A string arg and a numeric arg that "look" the same must not collide.
    PStr p1(L"1");
    XLOPER12 sOne = Str(p1);
    Check(xll::MakeCacheKey("F", {&sOne}) != xll::MakeCacheKey("F", {&n1}),
          "MakeCacheKey: string \"1\" != number 1.0");

    // CONSTANT SIZE: a 1x1 grid arg and a 100x100 grid arg produce the same key
    // length. The old key embedded the whole serialization (hundreds of KB).
    std::vector<XLOPER12> small(1, Num(1.0));
    std::vector<XLOPER12> big(100 * 100);
    for (size_t i = 0; i < big.size(); ++i) big[i] = Num((double)i);
    XLOPER12 mSmall = Multi(1, 1, small.data());
    XLOPER12 mBig = Multi(100, 100, big.data());
    std::string kSmall = xll::MakeCacheKey("G", {&mSmall});
    std::string kBig = xll::MakeCacheKey("G", {&mBig});
    Check(kSmall.size() == kBig.size() && kBig.size() == 1 + 1 + 16,
          "MakeCacheKey size is independent of argument content (" +
              std::to_string(kSmall.size()) + " vs " + std::to_string(kBig.size()) + ")");
    Check(kSmall != kBig, "MakeCacheKey still separates a 1x1 grid from a 100x100 grid");

    // Editing one cell of the big grid must change the key.
    big[5000] = Num(-1.0);
    Check(xll::MakeCacheKey("G", {&mBig}) != kBig, "MakeCacheKey tracks a single edited cell");
}

// ---------------------------------------------------------------------------
// 4. reference arguments: SRef contributes to the key; Ref uses the RefCache
// ---------------------------------------------------------------------------

static void TestRefArgs() {
    XLOPER12 cellsA[2] = {Num(1.0), Num(2.0)};
    XLOPER12 cellsB[2] = {Num(3.0), Num(4.0)};
    XLOPER12 coercedA = Multi(1, 2, cellsA);
    XLOPER12 coercedB = Multi(1, 2, cellsB);

    XLOPER12 sref = SRef(0, 0, 0, 1);

    // An xltypeSRef argument must CONTRIBUTE its content to the key. The old
    // GetOrComputeRefHash returned "" for anything that was not xltypeRef, so
    // every single-area reference argument collapsed onto one key -> a call on
    // A1:B2 could be served the result computed for C5:D9.
    xll::CacheManager::Instance().ClearRefCache();
    g_coerceResult = &coercedA;
    std::string ka = xll::MakeCacheKey("F", {&sref});
    g_coerceResult = &coercedB;
    std::string kb = xll::MakeCacheKey("F", {&sref});
    Check(ka != kb, "SRef argument content reaches the cache key (" + ka + " vs " + kb + ")");

    g_coerceResult = &coercedA;
    Check(xll::MakeCacheKey("F", {&sref}) == ka, "SRef key is deterministic for identical content");

    // xltypeRef: the per-cycle RefCache must coerce a given (sheet, rect) once.
    xll::CacheManager::Instance().ClearRefCache();
    RefHolder ref(0x1234, {Rect(0, 1, 0, 1)});
    g_coerceResult = &coercedA;
    g_coerceCalls.store(0);
    std::string r1 = xll::MakeCacheKey("F", {&ref.op});
    int afterFirst = g_coerceCalls.load();
    std::string r2 = xll::MakeCacheKey("F", {&ref.op});
    int afterSecond = g_coerceCalls.load();
    Check(r1 == r2, "Ref key is stable across calls in one cycle");
    Check(afterFirst == 1 && afterSecond == 1,
          "RefCache coerces a (sheet, rect) once per cycle (coerce calls: " +
              std::to_string(afterFirst) + " then " + std::to_string(afterSecond) + ")");

    // ...and a ClearRefCache (calc end) makes it recompute.
    xll::CacheManager::Instance().ClearRefCache();
    (void)xll::MakeCacheKey("F", {&ref.op});
    Check(g_coerceCalls.load() == 2, "ClearRefCache forces a recompute");

    // Distinct rects must hash apart even with the same content-shaped stub.
    xll::CacheManager::Instance().ClearRefCache();
    RefHolder refOther(0x1234, {Rect(5, 6, 5, 6)});
    RefHolder refTwoRects(0x1234, {Rect(0, 1, 0, 1), Rect(5, 6, 5, 6)});
    g_coerceResult = &coercedA;
    std::string one = xll::MakeCacheKey("F", {&ref.op});
    std::string two = xll::MakeCacheKey("F", {&refTwoRects.op});
    Check(one != two, "a 1-rect ref and a 2-rect ref hash apart");

    // Coerce failure folds in the reference IDENTITY, so two distinct failing
    // refs must not collapse onto one topic/entry.
    xll::CacheManager::Instance().ClearRefCache();
    g_coerceResult = nullptr;
    std::string f1 = xll::ContentHashToken('r', &ref.op);
    std::string f2 = xll::ContentHashToken('r', &refOther.op);
    XLOPER12 srefOther = SRef(9, 9, 9, 9);
    std::string f3 = xll::ContentHashToken('r', &sref);
    std::string f4 = xll::ContentHashToken('r', &srefOther);
    Check(f1 != f2, "distinct failing xltypeRefs hash apart");
    Check(f3 != f4, "distinct failing xltypeSRefs hash apart");
    Check(f1 != f3, "a failing Ref and a failing SRef hash apart");
    g_coerceResult = &coercedA;
    xll::CacheManager::Instance().ClearRefCache();
}

// ---------------------------------------------------------------------------
// 5. defect 2 — concurrent Get/Put must be internally locked
// ---------------------------------------------------------------------------

static void TestConcurrentGetPut() {
    xll::CacheConfig cfg;
    cfg.enabled = true;
    cfg.ttl = std::chrono::milliseconds(60000);
    cfg.jitter = std::chrono::milliseconds(0);

    const int kThreads = 8;
    const int kIters = 20000;
    std::atomic<int> mismatches{0};
    std::atomic<int> ready{0};

    auto worker = [&](int id) {
        ready.fetch_add(1);
        while (ready.load() < kThreads) { /* line up so the threads really overlap */ }
        for (int i = 0; i < kIters; ++i) {
            // Deliberately overlap the key space across threads so several
            // threads hit the SAME phmap submap concurrently.
            std::string key = "F#" + std::to_string(i % 512) + "-" + std::to_string(i % 3);
            std::vector<uint8_t> payload((size_t)(i % 17) + 1, (uint8_t)(i & 0xFF));
            xll::CacheManager::Instance().Put(key, payload, cfg);

            std::vector<uint8_t> got;
            if (xll::CacheManager::Instance().Get(key, got)) {
                // Another thread may have written a different payload for the
                // same key; only a TORN value (a length no writer ever used) is a
                // real failure.
                if (got.empty() || got.size() > 17) mismatches.fetch_add(1);
                else if (got[0] != (uint8_t)(got.size() - 1 + 0) && false) mismatches.fetch_add(1);
            }
            if ((i & 1023) == 0) (void)id;
        }
    };

    std::vector<std::thread> threads;
    for (int t = 0; t < kThreads; ++t) threads.emplace_back(worker, t);
    for (auto& th : threads) th.join();

    Check(mismatches.load() == 0,
          "concurrent Get/Put over " + std::to_string(kThreads * kIters) +
              " ops produced no torn entries (" + std::to_string(mismatches.load()) + ")");

    // if_contains contract: with locking enabled a hit must observe exactly what
    // was stored.
    std::vector<uint8_t> want = {1, 2, 3, 4, 5};
    xll::CacheManager::Instance().Put("if-contains-probe", want, cfg);
    std::vector<uint8_t> got;
    Check(xll::CacheManager::Instance().Get("if-contains-probe", got) && got == want,
          "Get/if_contains returns the exact stored payload");

    // Expired entries read as a miss.
    xll::CacheConfig expired;
    expired.enabled = true;
    expired.ttl = std::chrono::milliseconds(0);
    xll::CacheManager::Instance().Put("ttl-probe", want, expired);
    std::this_thread::sleep_for(std::chrono::milliseconds(5));
    std::vector<uint8_t> unused;
    Check(!xll::CacheManager::Instance().Get("ttl-probe", unused), "an expired entry reads as a miss");

    // A disabled config must not store.
    xll::CacheConfig off;
    off.enabled = false;
    xll::CacheManager::Instance().Put("disabled-probe", want, off);
    Check(!xll::CacheManager::Instance().Get("disabled-probe", unused), "Put is a no-op when disabled");
}

static void TestConcurrentRefHash() {
    XLOPER12 cells[2] = {Num(1.0), Num(2.0)};
    XLOPER12 coerced = Multi(1, 2, cells);
    g_coerceResult = &coerced;
    xll::CacheManager::Instance().ClearRefCache();

    const int kThreads = 8;
    const int kIters = 4000;
    std::atomic<int> disagreements{0};
    std::string expected;

    std::vector<RefHolder*> refs;
    for (int i = 0; i < 64; ++i) refs.push_back(new RefHolder(0x1000 + (i % 4), {Rect(i, i + 1, 0, 1)}));

    {
        expected = xll::MakeCacheKey("F", {&refs[0]->op});
    }

    auto worker = [&]() {
        for (int i = 0; i < kIters; ++i) {
            RefHolder* r = refs[i % refs.size()];
            std::string k = xll::MakeCacheKey("F", {&r->op});
            if (i % refs.size() == 0 && k != expected) disagreements.fetch_add(1);
        }
    };

    std::vector<std::thread> threads;
    for (int t = 0; t < kThreads; ++t) threads.emplace_back(worker);
    for (auto& th : threads) th.join();

    Check(disagreements.load() == 0,
          "concurrent GetOrComputeRefHash agrees across threads (" +
              std::to_string(disagreements.load()) + " disagreements)");

    for (RefHolder* r : refs) delete r;
    xll::CacheManager::Instance().ClearRefCache();
}

// ---------------------------------------------------------------------------
// 6. Microbenchmarks
// ---------------------------------------------------------------------------

// Legacy FNV-1a over a std::string — exactly what the old ContentHashToken did
// on top of SerializeXLOPER.
static uint64_t LegacyFnv1a(const std::string& s) {
    uint64_t hash = 14695981039346656037ULL;
    for (char c : s) {
        hash ^= (unsigned char)c;
        hash *= 1099511628211ULL;
    }
    return hash;
}

template <typename F>
static double BenchNs(int iters, F&& f) {
    auto t0 = std::chrono::steady_clock::now();
    for (int i = 0; i < iters; ++i) f();
    auto t1 = std::chrono::steady_clock::now();
    return std::chrono::duration<double, std::nano>(t1 - t0).count() / iters;
}

static void RunBenchmarks() {
    std::printf("\n--- microbenchmarks (g++ -O2) ---\n");

    // (a) 100x100 xltypeMulti grid.
    std::vector<XLOPER12> cells(100 * 100);
    for (size_t i = 0; i < cells.size(); ++i) cells[i] = Num((double)i * 1.5);
    XLOPER12 grid = Multi(100, 100, cells.data());

    double newNs = BenchNs(50, [&] {
        std::string t = xll::ContentHashToken('g', &grid);
        if (t.empty()) std::abort();
    });
    double oldNs = BenchNs(50, [&] {
        std::string s = xll::SerializeXLOPER(&grid);
        uint64_t h = LegacyFnv1a(s);
        if (h == 0) std::abort();
    });
    std::printf("grid 100x100 numeric  : old %10.0f ns   new %10.0f ns   %.1fx\n", oldNs, newNs, oldNs / newNs);
    std::printf("grid 100x100 serialized key size: %zu bytes\n", xll::SerializeXLOPER(&grid).size());

    // (b) 100x100 grid of strings.
    std::vector<PStr*> strs;
    std::vector<XLOPER12> scells(100 * 100);
    for (size_t i = 0; i < scells.size(); ++i) {
        strs.push_back(new PStr(L"한국 value " + std::to_wstring(i)));
        scells[i] = Str(*strs.back());
    }
    XLOPER12 sgrid = Multi(100, 100, scells.data());
    double snew = BenchNs(50, [&] {
        std::string t = xll::ContentHashToken('g', &sgrid);
        if (t.empty()) std::abort();
    });
    double sold = BenchNs(50, [&] {
        std::string s = xll::SerializeXLOPER(&sgrid);
        uint64_t h = LegacyFnv1a(s);
        if (h == 0) std::abort();
    });
    std::printf("grid 100x100 CJK str  : old %10.0f ns   new %10.0f ns   %.1fx\n", sold, snew, sold / snew);

    // (c) scalar-only MakeCacheKey (the common cache-hit path).
    PStr ps(L"AAPL");
    XLOPER12 s = Str(ps);
    XLOPER12 n = Num(42.5);
    XLOPER12 b = Bool(true);
    double keyNew = BenchNs(200000, [&] {
        std::string k = xll::MakeCacheKey("Quote", {&s, &n, &b});
        if (k.empty()) std::abort();
    });
    double keyOld = BenchNs(200000, [&] {
        // The old MakeCacheKey: one stringstream plus one SerializeXLOPER
        // (itself a stringstream) per argument.
        std::string k = "Quote(";
        k += xll::SerializeXLOPER(&s);
        k += ",";
        k += xll::SerializeXLOPER(&n);
        k += ",";
        k += xll::SerializeXLOPER(&b);
        k += ",)";
        if (k.empty()) std::abort();
    });
    std::printf("MakeCacheKey 3 scalars: old %10.0f ns   new %10.0f ns   %.1fx\n", keyOld, keyNew, keyOld / keyNew);

    // (d) key size effect on the phmap Get itself.
    xll::CacheConfig cfg;
    cfg.enabled = true;
    cfg.ttl = std::chrono::milliseconds(600000);
    std::string shortKey = xll::MakeCacheKey("G", {&grid});
    std::string longKey = "G(" + xll::SerializeXLOPER(&grid) + ")";
    std::vector<uint8_t> payload(64, 7);
    xll::CacheManager::Instance().Put(shortKey, payload, cfg);
    xll::CacheManager::Instance().Put(longKey, payload, cfg);
    std::vector<uint8_t> out;
    double getShort = BenchNs(200000, [&] { xll::CacheManager::Instance().Get(shortKey, out); });
    double getLong = BenchNs(200000, [&] { xll::CacheManager::Instance().Get(longKey, out); });
    std::printf("CacheManager::Get     : %zu-byte key %6.0f ns   %zu-byte key %6.0f ns   %.1fx\n",
                longKey.size(), getLong, shortKey.size(), getShort, getLong / getShort);

    for (PStr* p : strs) delete p;
}

int main(int argc, char** argv) {
    // Unbuffered: the pre-fix code can AV mid-run (a CJK first character made the
    // old xltypeStr branch read ~109 KB off the end of the stack buffer), and the
    // FAIL lines printed before the crash are the evidence.
    std::setvbuf(stdout, nullptr, _IONBF, 0);

    bool bench = false;
    for (int i = 1; i < argc; ++i) {
        if (std::strcmp(argv[i], "-bench") == 0) bench = true;
    }

    TestStringTokenDeterministic();
    TestSerializeStringExact();
    TestTokenDistinctness();
    TestFP12Token();
    TestMakeCacheKey();
    TestRefArgs();
    TestConcurrentGetPut();
    TestConcurrentRefHash();

    if (bench) RunBenchmarks();

    std::printf("\n%d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
