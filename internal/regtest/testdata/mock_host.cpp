#include <iostream>
#include <vector>
#include <thread>
#include <chrono>
#include <cmath>
#include "shm/DirectHost.h"
// protocol_generated.h now ships in the `types` library (extracted in v0.1.0);
// consume it via the types/ prefix to match the generated XLL sources.
#include "types/protocol_generated.h"
#include "schema_generated.h"
// Ribbon image decode path (test 15). com/ribbon_image.h pulls in windows.h +
// ocidl.h (IPictureDisp) itself; ribbon_images.h is the generated embed table.
#include "com/ribbon_image.h"
#include "ribbon_images.h"

using namespace std;

#define ASSERT_EQ(a, b, msg) { \
    if ((a) != (b)) { \
        cerr << "FAIL: " << msg << " Expected " << (a) << " got " << (b) << endl; \
        return 1; \
    } \
}
#define ASSERT_STREQ(a, b, msg) { \
    if (string(a) != string(b)) { \
        cerr << "FAIL: " << msg << " Expected '" << (a) << "' got '" << (b) << "'" << endl; \
        return 1; \
    } \
}

// main is the entry point for the mock host.
// It initializes shared memory, acts as the Excel process, sends various requests to the Go server,
// and verifies the responses.
int main(int argc, char* argv[]) {
    shm::DirectHost host;
    shm::HostConfig config;
    config.shmName = "smoke_proj";
    if (argc > 1) {
        config.shmName = argv[1];
    }
    config.numHostSlots = 16;
    config.numGuestSlots = 2;
    config.payloadSize = 1024*1024;

    if (!host.Init(config)) {
        cerr << "Failed to init SHM" << endl;
        return 1;
    }
    cout << "READY" << endl;

    flatbuffers::FlatBufferBuilder builder(1024);

    // 1. EchoInt (ID 140)
    vector<int32_t> intCases = {0, 1, -1, 2147483647, (int32_t)-2147483648LL};
    for (size_t i = 0; i < intCases.size(); ++i) {
        auto val = intCases[i];
        builder.Reset();
        ipc::EchoIntRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = -1;

        // Retry logic for the first request to allow Guest to connect
        if (i == 0) {
             auto startWait = chrono::steady_clock::now();
             int spin = 0;
             while(chrono::steady_clock::now() - startWait < chrono::seconds(30)) {
                sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)140, respBuf).ValueOr(-1);
                if (sz >= 0) break;
                if (spin < 1000) {
                    this_thread::yield();
                    spin++;
                } else {
                    this_thread::sleep_for(chrono::milliseconds(1));
                    spin = 0;
                }
             }
        } else {
             sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)140, respBuf).ValueOr(-1);
        }

        if (sz < 0) { cerr << "Send failed for EchoInt " << val << endl; return 1; }

        auto resp = flatbuffers::GetRoot<ipc::EchoIntResponse>(respBuf.data());
        if (resp->error() && resp->error()->size() > 0) { cerr << "Error: " << resp->error()->str() << endl; return 1; }
        ASSERT_EQ(val, resp->result(), "EchoInt");
    }

    // 2. EchoFloat (ID 141)
    vector<double> floatCases = {0.0, 1.5, -999.99};
    for (auto val : floatCases) {
        builder.Reset();
        ipc::EchoFloatRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)141, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatResponse>(respBuf.data());
        if (std::abs(val - resp->result()) > 0.0001) { cerr << "Float mismatch" << endl; return 1; }
    }

    // 3. EchoString (ID 142)
    vector<string> strCases = {"test", "", "Hello World"};
    for (auto val : strCases) {
        builder.Reset();
        auto off = builder.CreateString(val);
        ipc::EchoStringRequestBuilder req(builder);
        req.add_val(off);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)142, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringResponse>(respBuf.data());
        ASSERT_STREQ(val, resp->result()->str(), "EchoString");
    }

    // 4. EchoBool (ID 143)
    vector<bool> boolCases = {true, false};
    for (auto val : boolCases) {
        builder.Reset();
        ipc::EchoBoolRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)143, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolResponse>(respBuf.data());
        ASSERT_EQ(val, resp->result(), "EchoBool");
    }

    // 5. CheckAny (ID 144)
    // Int
    {
        builder.Reset();
        auto val = protocol::CreateInt(builder, 10);
        auto any = protocol::CreateAny(builder, protocol::AnyValue::Int, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Int:10", resp->result()->str(), "CheckAny Int");
    }
    // Str
    {
        builder.Reset();
        auto s = builder.CreateString("hello");
        auto val = protocol::CreateStr(builder, s);
        auto any = protocol::CreateAny(builder, protocol::AnyValue::Str, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Str:hello", resp->result()->str(), "CheckAny Str");
    }
    // Num
    {
        builder.Reset();
        auto val = protocol::CreateNum(builder, 1.5);
        auto any = protocol::CreateAny(builder, protocol::AnyValue::Num, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Num:1.5", resp->result()->str(), "CheckAny Num");
    }

    // NumGrid
    {
        builder.Reset();
        std::vector<double> data = {1.1, 2.2};
        auto dataOff = builder.CreateVector(data);
        auto arr = protocol::CreateNumGrid(builder, 1, 2, dataOff);
        auto any = protocol::CreateAny(builder, protocol::AnyValue::NumGrid, arr.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("NumGrid:1x2", resp->result()->str(), "CheckAny NumGrid");
    }

    // Grid
    {
        builder.Reset();
        auto val1 = protocol::CreateInt(builder, 1);
        auto s1 = protocol::CreateScalar(builder, protocol::ScalarValue::Int, val1.Union());
        auto val2 = protocol::CreateBool(builder, true);
        auto s2 = protocol::CreateScalar(builder, protocol::ScalarValue::Bool, val2.Union());

        std::vector<flatbuffers::Offset<protocol::Scalar>> data = {s1, s2};
        auto dataOff = builder.CreateVector(data);
        auto arr = protocol::CreateGrid(builder, 1, 2, dataOff);
        auto any = protocol::CreateAny(builder, protocol::AnyValue::Grid, arr.Union());

        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Grid:1x2", resp->result()->str(), "CheckAny Grid");
    }

    // 6. CheckRange (ID 145)
    {
        builder.Reset();
        auto sOff = builder.CreateString("Sheet1");
        std::vector<protocol::Rect> refs = { {1,1,1,1} };
        auto refsOff = builder.CreateVectorOfStructs(refs);
        auto rangeVal = protocol::CreateRange(builder, sOff, refsOff);
        ipc::CheckRangeRequestBuilder req(builder);
        req.add_val(rangeVal);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)145, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckRangeResponse>(respBuf.data());
        ASSERT_STREQ("Range:Sheet1!1:1:1:1", resp->result()->str(), "CheckRange");
    }

    // 7. TimeoutFunc (ID 146)
    {
        builder.Reset();
        ipc::TimeoutFuncRequestBuilder req(builder);
        req.add_val(10);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)146, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::TimeoutFuncResponse>(respBuf.data());

        // Timeout now returns -1 instead of error
        ASSERT_EQ(-1, resp->result(), "TimeoutFunc");
    }

    // 8. AsyncEchoInt (ID 147)
    // Async requests have a different flow:
    // 1. Send Request -> Receive ACK (immediately)
    // 2. Poll for BatchAsyncResponse (MSG_ID 128)
    {
        int32_t val = 999;

        // Construct Async Handle (simulate 32-byte XLOPER12 struct)
        std::vector<uint8_t> handle(32, 0);
        handle[0] = 0xAA; // Marker to verify echo

        builder.Reset();
        auto hOff = builder.CreateVector(handle);
        ipc::AsyncEchoIntRequestBuilder req(builder);
        req.add_val(val);
        req.add_async_handle(hOff);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;

        // 1. Send Request -> Expect ACK
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)147, respBuf).ValueOr(-1);
        if (sz < 0) return 1;
        auto ack = flatbuffers::GetRoot<protocol::Ack>(respBuf.data());
        if (!ack->ok()) { cerr << "AsyncEchoInt Ack failed" << endl; return 1; }

        // 2. Wait for Async Response (MSG_BATCH_ASYNC_RESPONSE = 128)
        // Since mock host is not running a loop, we rely on the Guest to send it.
        // Guest sends it unsolicited. DirectHost receives it via ProcessGuestCalls or we need to peek?
        // Wait, DirectHost.Send is synchronous.
        // But for unsolicited messages from Guest (like BatchAsyncResponse),
        // DirectHost usually needs a listening mechanism or we call Receive?
        // `host.Send` sends a request and waits for a response on the SAME slot.
        // Async results come on a DIFFERENT slot or as a separate message?
        // The Guest sends BatchAsyncResponse (128) to the Host.
        // In real XLL, `ProcessGuestCalls` handles this.
        // Here, we can simulate `ProcessGuestCalls` by checking the guest slots.

        bool received = false;
        auto start = chrono::steady_clock::now();
        while(chrono::steady_clock::now() - start < chrono::seconds(5)) {
             // Iterate guest slots to find pending messages
             // DirectHost API doesn't expose manual slot iteration easily without `ProcessGuestCalls`.
             // But we can use `ProcessGuestCalls` with a callback.

             host.ProcessGuestCalls([&](const uint8_t* data, int32_t size, uint8_t* respBuf, int32_t maxRespSize, shm::MsgType type) -> int32_t {
                 if (type == (shm::MsgType)128) { // MSG_BATCH_ASYNC_RESPONSE
                     auto batch = flatbuffers::GetRoot<protocol::BatchAsyncResponse>(data);
                     if (batch->results()->size() > 0) {
                         const protocol::AsyncResult* res = batch->results()->Get(0);

                         // Verify Handle
                         // res->handle() returns a pointer to Vector<uint8_t> in standard FlatBuffers.
                         // However, if strict alignment or some other flag is used, it might return a Span or similar.
                         // But we are using default flatc.
                         // If the compiler says "base operand of -> is not a pointer", it means res->handle() IS NOT A POINTER.
                         // So we try dot notation.
                         if (res->handle()->size() != 32) { cerr << "Invalid handle size" << endl; return 0; }
                         if (res->handle()->Get(0) != 0xAA) { cerr << "Invalid handle content" << endl; return 0; }

                         // Verify Result
                         if (res->result()->val_type() != protocol::AnyValue::Int) { cerr << "Invalid result type" << endl; return 0; }
                         if (res->result()->val_as_Int()->val() != val) { cerr << "Invalid result value" << endl; return 0; }

                         received = true;
                         return 1; // Handled
                     }
                 }
                 return 0;
             });

             if (received) break;
             this_thread::sleep_for(chrono::milliseconds(10));
        }

        if (!received) { cerr << "AsyncEchoInt timed out" << endl; return 1; }
    }

    // 9. CalculationEnded Commands - Set (ID 148)
    {
        // 1. Call ScheduleCmd (ID 148)
        builder.Reset();
        ipc::ScheduleCmdRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)148, respBuf).ValueOr(-1) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleCmdResponse>(respBuf.data());
        if (resp->error() && resp->error()->size() > 0) { cerr << "ScheduleCmd Error: " << resp->error()->str() << endl; }
        cerr << "ScheduleCmd Result: " << resp->result() << endl;
        ASSERT_EQ(1, resp->result(), "ScheduleCmd");

        // 2. Send CalculationEnded (ID 131)
        // 2. Send CalculationEnded (ID 131)
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, (shm::MsgType)131, eventBuf).ValueOr(-1) < 0) return 1;

        // 3. Verify Response contains SetCommand
        if (eventBuf.empty()) { cerr << "Expected commands in CalcEnded response" << endl; return 1; }
        auto eventResp = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(eventBuf.data());

        if (!eventResp->commands()) { cerr << "No commands list" << endl; return 1; }
        if (eventResp->commands()->size() != 1) { cerr << "Expected 1 command" << endl; return 1; }

        auto wrapper = eventResp->commands()->Get(0);
        if (wrapper->cmd_type() != protocol::Command::SetCommand) { cerr << "Expected SetCommand" << endl; return 1; }

        auto setCmd = static_cast<const protocol::SetCommand*>(wrapper->cmd());
        auto rng = setCmd->target();
        ASSERT_STREQ("Sheet1", rng->sheet_name()->str(), "SetCommand Sheet");

        auto val = setCmd->value();
        ASSERT_EQ((int)protocol::AnyValue::Int, (int)val->val_type(), "SetCommand ValType");
        ASSERT_EQ(100, val->val_as_Int()->val(), "SetCommand Val");
    }

    // 10. CalculationEnded Commands - Format (ID 149)
    {
        // 1. Call ScheduleFormatCmd (ID 149)
        builder.Reset();
        ipc::ScheduleFormatCmdRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)149, respBuf).ValueOr(-1) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleFormatCmdResponse>(respBuf.data());
        ASSERT_EQ(1, resp->result(), "ScheduleFormatCmd");

        // 2. Send CalculationEnded
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, (shm::MsgType)131, eventBuf).ValueOr(-1) < 0) return 1;

        // 3. Verify Response contains FormatCommand
        auto eventResp = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(eventBuf.data());
        if (eventResp->commands()->size() != 1) { cerr << "Expected 1 format command" << endl; return 1; }

        auto wrapper = eventResp->commands()->Get(0);
        if (wrapper->cmd_type() != protocol::Command::FormatCommand) { cerr << "Expected FormatCommand" << endl; return 1; }

        auto fmtCmd = static_cast<const protocol::FormatCommand*>(wrapper->cmd());
        auto rng = fmtCmd->target();
        ASSERT_STREQ("Sheet1", rng->sheet_name()->str(), "FormatCommand Sheet");

        ASSERT_STREQ("General", fmtCmd->format()->str(), "FormatCommand Format");
    }

    // 11. CalculationEnded Commands - Multi (ID 150)
    {
        // 1. Call ScheduleMultiCmd (ID 150)
        builder.Reset();
        ipc::ScheduleMultiCmdRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)150, respBuf).ValueOr(-1) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleMultiCmdResponse>(respBuf.data());
        ASSERT_EQ(2, resp->result(), "ScheduleMultiCmd");

        // 2. Send CalculationEnded
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, (shm::MsgType)131, eventBuf).ValueOr(-1) < 0) return 1;

        // 3. Verify Response contains 2 commands (Set, Format)
        auto eventResp = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(eventBuf.data());
        if (eventResp->commands()->size() != 2) { cerr << "Expected 2 commands" << endl; return 1; }

        // First: Set
        {
            auto wrapper = eventResp->commands()->Get(0);
            if (wrapper->cmd_type() != protocol::Command::SetCommand) { cerr << "Expected SetCommand 1st" << endl; return 1; }
            auto setCmd = static_cast<const protocol::SetCommand*>(wrapper->cmd());
            auto val = setCmd->value();
            ASSERT_EQ(200, val->val_as_Int()->val(), "Multi SetCommand Val");
        }
        // Second: Format
        {
            auto wrapper = eventResp->commands()->Get(1);
            if (wrapper->cmd_type() != protocol::Command::FormatCommand) { cerr << "Expected FormatCommand 2nd" << endl; return 1; }
            auto fmtCmd = static_cast<const protocol::FormatCommand*>(wrapper->cmd());
            ASSERT_STREQ("Number", fmtCmd->format()->str(), "Multi FormatCommand Format");
        }
    }

    // 11. ScheduleMassive (ID 151)
    {
        // 1. Call ScheduleMassive
        builder.Reset();
        ipc::ScheduleMassiveRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)151, respBuf).ValueOr(-1) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleMassiveResponse>(respBuf.data());
        ASSERT_EQ(100, resp->result(), "ScheduleMassive");

        // 2. Send CalculationEnded
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, (shm::MsgType)131, eventBuf).ValueOr(-1) < 0) return 1;

        // 3. Verify Response
        auto eventResp = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(eventBuf.data());
        if (!eventResp->commands()) { cerr << "No commands list for Massive" << endl; return 1; }

        int cmdCount = eventResp->commands()->size();
        if (cmdCount != 4) {
            cerr << "Expected 4 commands for massive checkerboard, got " << cmdCount << endl;
            return 1;
        }

        int count100 = 0;
        int count200 = 0;

        for (unsigned int i=0; i<eventResp->commands()->size(); ++i) {
             auto wrapper = eventResp->commands()->Get(i);
             if (wrapper->cmd_type() == protocol::Command::SetCommand) {
                 auto setCmd = static_cast<const protocol::SetCommand*>(wrapper->cmd());
                 auto val = setCmd->value()->val_as_Int()->val();
                 if (val == 100) count100++;
                 else if (val == 200) count200++;
             }
        }
        ASSERT_EQ(2, count100, "Count 100 commands");
        ASSERT_EQ(2, count200, "Count 200 commands");
    }

    // 12. ScheduleGridCmd (ID 152)
    {
        // 1. Call ScheduleGridCmd
        builder.Reset();
        ipc::ScheduleGridCmdRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)152, respBuf).ValueOr(-1) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleGridCmdResponse>(respBuf.data());
        ASSERT_EQ(1, resp->result(), "ScheduleGridCmd");

        // 2. Send CalculationEnded
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, (shm::MsgType)131, eventBuf).ValueOr(-1) < 0) return 1;

        // 3. Verify Response contains Grid
        auto eventResp = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(eventBuf.data());
        if (!eventResp->commands()) { cerr << "No commands list for Grid" << endl; return 1; }
        if (eventResp->commands()->size() != 1) { cerr << "Expected 1 command for Grid, got " << eventResp->commands()->size() << endl; return 1; }

        auto wrapper = eventResp->commands()->Get(0);
        auto setCmd = static_cast<const protocol::SetCommand*>(wrapper->cmd());
        auto val = setCmd->value();

        if (val->val_type() != protocol::AnyValue::Grid) {
            cerr << "Expected Grid, got " << (int)val->val_type() << endl;
            return 1;
        }

        auto grid = val->val_as_Grid();
        ASSERT_EQ(2, grid->rows(), "Grid Rows");
        ASSERT_EQ(2, grid->cols(), "Grid Cols");

        // Data: [[1, 2], [3, 4]]
        if (grid->data()->size() != 4) { cerr << "Expected 4 scalars" << endl; return 1; }

        auto s0 = grid->data()->Get(0);
        ASSERT_EQ((int)protocol::ScalarValue::Int, (int)s0->val_type(), "S0 type");
        ASSERT_EQ(1, s0->val_as_Int()->val(), "S0 val");

        auto s1 = grid->data()->Get(1);
        ASSERT_EQ((int)protocol::ScalarValue::Int, (int)s1->val_type(), "S1 type");
        ASSERT_EQ(2, s1->val_as_Int()->val(), "S1 val");

        auto s3 = grid->data()->Get(3);
        ASSERT_EQ(4, s3->val_as_Int()->val(), "S3 val");
    }

    // 13. RefCache Cleanup on Canceled (ID 130, 132, 131, 144)
    // Product decision (IMPROVEMENT_BACKLOG.md §7 / refcache_test.go
    // TestHandleCalculationCanceled_ClearsRefCache): a CANCELED calc clears the
    // RefCache, symmetric with calc-ENDED. This case asserts BOTH clear paths.
    {
        // Helper: set RefCache "K1" = Int(123) and assert it resolves via CheckAny.
        auto setK1 = [&](vector<uint8_t>& respBuf) -> bool {
            builder.Reset();
            auto keyOff = builder.CreateString("K1");
            auto valOff = protocol::CreateInt(builder, 123);
            auto anyOff = protocol::CreateAny(builder, protocol::AnyValue::Int, valOff.Union());
            auto req = protocol::CreateSetRefCacheRequest(builder, keyOff, anyOff);
            builder.Finish(req);
            if (host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)130, respBuf).ValueOr(-1) < 0) return false;
            auto ack = flatbuffers::GetRoot<protocol::Ack>(respBuf.data());
            return ack->ok();
        };
        // Helper: CheckAny on RefCache("K1") and return the rendered result string.
        auto checkK1 = [&](vector<uint8_t>& respBuf) -> string {
            builder.Reset();
            auto keyOff = builder.CreateString("K1");
            auto rcVal = protocol::CreateRefCache(builder, keyOff);
            auto anyOff = protocol::CreateAny(builder, protocol::AnyValue::RefCache, rcVal.Union());
            ipc::CheckAnyRequestBuilder caReq(builder);
            caReq.add_val(anyOff);
            builder.Finish(caReq.Finish());
            if (host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1) < 0) return string();
            auto caResp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
            return caResp->result()->str();
        };

        vector<uint8_t> respBuf;

        // --- Canceled clears the cache ---
        // 1. Set "K1" and confirm it resolves to "Int:123".
        ASSERT_EQ(true, setK1(respBuf), "SetRefCache Ack (canceled case)");
        ASSERT_STREQ("Int:123", checkK1(respBuf).c_str(), "CheckAny RefCache Resolved (pre-cancel)");

        // 2. Send CalculationCanceled (ID 132); no response payload expected.
        if(host.Send(nullptr, 0, (shm::MsgType)132, respBuf).ValueOr(-1) < 0) return 1;

        // 3. Cache must now be cleared -> CheckAny renders the unresolved key.
        ASSERT_STREQ("RefCache:K1", checkK1(respBuf).c_str(), "CheckAny RefCache Cleared on Cancel");

        // --- Ended also clears the cache ---
        // 4. Re-establish "K1" so the calc-ended path has something to clear.
        ASSERT_EQ(true, setK1(respBuf), "SetRefCache Ack (ended case)");
        ASSERT_STREQ("Int:123", checkK1(respBuf).c_str(), "CheckAny RefCache Resolved (pre-end)");

        // 5. Send CalculationEnded (ID 131).
        if(host.Send(nullptr, 0, (shm::MsgType)131, respBuf).ValueOr(-1) < 0) return 1;

        // 6. Cache must be cleared after Ended too.
        ASSERT_STREQ("RefCache:K1", checkK1(respBuf).c_str(), "CheckAny RefCache Cleared on End");
    }

    // 14. Command invoke (MSG_COMMAND_INVOKE = 137)
    // The mock host plays the XLL/ribbon role: it sends a CommandInvokeRequest
    // and asserts the delivery ack. Handlers run fire-and-forget on the server,
    // so the ack (ok/error) is all that comes back over the same slot.
    {
        // 14a. Known command -> ok=true
        builder.Reset();
        auto req = protocol::CreateCommandInvokeRequestDirect(builder, "RunReport", "btn1");
        builder.Finish(req);
        vector<uint8_t> respBuf;
        if (host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)137, respBuf).ValueOr(-1) < 0) {
            cerr << "Send failed for CommandInvoke (known)" << endl;
            return 1;
        }
        auto resp = flatbuffers::GetRoot<protocol::CommandInvokeResponse>(respBuf.data());
        if (!resp->ok()) {
            cerr << "FAIL: CommandInvoke known: expected ok=true, error="
                 << (resp->error() ? resp->error()->str() : "") << endl;
            return 1;
        }

        // 14b. Unknown command -> ok=false
        builder.Reset();
        auto reqUnknown = protocol::CreateCommandInvokeRequestDirect(builder, "NoSuchCommand", "btn1");
        builder.Finish(reqUnknown);
        respBuf.clear();
        if (host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)137, respBuf).ValueOr(-1) < 0) {
            cerr << "Send failed for CommandInvoke (unknown)" << endl;
            return 1;
        }
        auto respUnknown = flatbuffers::GetRoot<protocol::CommandInvokeResponse>(respBuf.data());
        if (respUnknown->ok()) {
            cerr << "FAIL: CommandInvoke unknown: expected ok=false" << endl;
            return 1;
        }
    }

    // 15. Ribbon image decode (generated ribbon_images.h -> GDI+ -> IPictureDisp).
    // Purely local (no server round-trip): verifies the exact embed+decode
    // chain the XLL's loadImage callback uses, including alpha-capable PNG.
    {
        if (FAILED(CoInitialize(nullptr))) {
            cerr << "FAIL: CoInitialize for ribbon image test" << endl;
            return 1;
        }
        // Assert the embed table itself first so a generation regression
        // (empty table) reports as an embed bug, not a decode bug.
        auto embedded = GetXllRibbonImages();
        if (embedded.empty()) {
            cerr << "FAIL: GetXllRibbonImages() table is empty (icon.png not embedded?)" << endl;
            return 1;
        }
        xll::ribbon::SetRibbonImages(std::move(embedded));

        IPictureDisp* pic = xll::ribbon::CreateRibbonPicture(L"xllgen_img_0");
        if (!pic) {
            cerr << "FAIL: CreateRibbonPicture(xllgen_img_0) returned null" << endl;
            return 1;
        }
        IPicture* p = nullptr;
        if (FAILED(pic->QueryInterface(IID_IPicture, (void**)&p)) || !p) {
            cerr << "FAIL: IPictureDisp -> IPicture QI" << endl;
            return 1;
        }
        OLE_XSIZE_HIMETRIC w = 0;
        OLE_YSIZE_HIMETRIC h = 0;
        p->get_Width(&w);
        p->get_Height(&h);
        if (w <= 0 || h <= 0) {
            cerr << "FAIL: decoded picture has empty extent" << endl;
            return 1;
        }
        // Unknown names must fail cleanly (blank icon path), never crash.
        if (xll::ribbon::CreateRibbonPicture(L"no_such_image") != nullptr) {
            cerr << "FAIL: unknown image name must return null" << endl;
            return 1;
        }
        p->Release();
        pic->Release();
        xll::ribbon::ShutdownRibbonImageEngine();
        CoUninitialize();
    }

    // 16. Chunked host->guest delivery (MSG_CHUNK = 129) — reassembly contract.
    //
    // This is the end-to-end counterpart to pkg/server/manager_test.go: the
    // mock host plays the XLL, splitting an EchoString request (ID 142) into
    // protocol::Chunk frames the Go guest's HandleChunk must reassemble before
    // dispatching. It replaces the long-deferred "regtest duplicate-chunk case"
    // (AGENTS.md §23.3 / IMPROVEMENT_BACKLOG R8 residue) and extends it to the
    // OVERLAP case, which is what the exact-coverage completion contract
    // (shm SPECIFICATION.md §3.3.4) was added for.
    //
    // Response shapes: a non-final chunk answers protocol::Ack; the chunk that
    // completes the transfer answers whatever the dispatched user function
    // returns (here an ipc::EchoStringResponse). A refused chunk comes back as
    // MsgType::SYSTEM_ERROR, which shm's SlotAllocator turns into an Err — i.e.
    // host.Send(...).ValueOr(-1) yields -1.
    {
        // Serialize an EchoString request, then hand it over in pieces.
        const string echoVal = "chunked-reassembly-payload-0123456789";
        builder.Reset();
        {
            auto off = builder.CreateString(echoVal);
            ipc::EchoStringRequestBuilder req(builder);
            req.add_val(off);
            builder.Finish(req.Finish());
        }
        const vector<uint8_t> payload(builder.GetBufferPointer(),
                                      builder.GetBufferPointer() + builder.GetSize());
        const uint32_t total = (uint32_t)payload.size();
        const uint32_t split = total / 2; // first chunk [0,split), second [split,total)

        flatbuffers::FlatBufferBuilder chunkBuilder(1024);
        // sendChunk frames one protocol::Chunk and delivers it on MSG_CHUNK.
        // Returns the SHM response size (<0 when the guest answered
        // SYSTEM_ERROR).
        auto sendChunk = [&](uint64_t id, uint32_t offset, uint32_t len,
                             vector<uint8_t>& respBuf) -> int {
            chunkBuilder.Clear();
            auto dataOff = chunkBuilder.CreateVector(payload.data() + offset, len);
            protocol::ChunkBuilder cb(chunkBuilder);
            cb.add_id(id);
            cb.add_total_size(total);
            cb.add_offset(offset);
            cb.add_data(dataOff);
            cb.add_msg_type(142); // dispatch target once reassembled: EchoString
            // "XCHN" mirrors pkg/chunk.BuildFrame's file identifier.
            chunkBuilder.Finish(cb.Finish(), "XCHN");
            respBuf.clear();
            return host.Send(chunkBuilder.GetBufferPointer(), chunkBuilder.GetSize(),
                             (shm::MsgType)129, respBuf).ValueOr(-1);
        };

        // 16a. Well-formed two-chunk transfer completes and dispatches.
        {
            vector<uint8_t> respBuf;
            if (sendChunk(0xC0DE0001ull, 0, split, respBuf) < 0) {
                cerr << "FAIL: chunk 16a first chunk rejected" << endl; return 1;
            }
            auto ack = flatbuffers::GetRoot<protocol::Ack>(respBuf.data());
            if (!ack->ok()) { cerr << "FAIL: chunk 16a first chunk Ack not ok" << endl; return 1; }

            if (sendChunk(0xC0DE0001ull, split, total - split, respBuf) < 0) {
                cerr << "FAIL: chunk 16a final chunk rejected" << endl; return 1;
            }
            auto resp = flatbuffers::GetRoot<ipc::EchoStringResponse>(respBuf.data());
            ASSERT_STREQ(echoVal, resp->result()->str(), "Chunked EchoString");
        }

        // 16b. DUPLICATE chunk is idempotent: replaying the first chunk must
        // neither complete the transfer early nor corrupt the reassembly. This
        // is the R8 residue case — before offset dedup, the replay advanced the
        // received counter past total_size and dispatched with the tail still
        // zero-filled.
        {
            vector<uint8_t> respBuf;
            if (sendChunk(0xC0DE0002ull, 0, split, respBuf) < 0) {
                cerr << "FAIL: chunk 16b first chunk rejected" << endl; return 1;
            }
            // Replay of the exact same range: still an Ack, still incomplete.
            if (sendChunk(0xC0DE0002ull, 0, split, respBuf) < 0) {
                cerr << "FAIL: chunk 16b duplicate chunk rejected (an exact retransmit must be tolerated)" << endl; return 1;
            }
            auto dupAck = flatbuffers::GetRoot<protocol::Ack>(respBuf.data());
            if (!dupAck->ok()) { cerr << "FAIL: chunk 16b duplicate Ack not ok" << endl; return 1; }

            if (sendChunk(0xC0DE0002ull, split, total - split, respBuf) < 0) {
                cerr << "FAIL: chunk 16b final chunk rejected" << endl; return 1;
            }
            auto resp = flatbuffers::GetRoot<ipc::EchoStringResponse>(respBuf.data());
            ASSERT_STREQ(echoVal, resp->result()->str(), "Chunked EchoString after duplicate replay");
        }

        // 16c. OVERLAPPING chunk is refused, AND the refusal is FINAL. [0,split)
        // then a range starting inside it: two distinct offsets whose lengths
        // can still sum to total_size, so the old `received >= total` test would
        // have reported COMPLETE with an interior region nobody ever wrote
        // (zero-fill read back as payload). Must come back SYSTEM_ERROR.
        //
        // The follow-on assertion is the POISON-SET contract. Dropping the
        // buffer alone made the rejection non-final: the producer's next chunk
        // found no buffer, the guest allocated a FRESH one, and the chunk was
        // ACKED — so the transfer silently restarted mid-stream (permanently
        // incomplete, holding a concurrency slot until the TTL) and
        // pkg/chunk.AsyncRetry, seeing success on its first retry, kept pushing
        // instead of failing the call. Every chunk on a rejected id must now
        // come back SYSTEM_ERROR until the poison entry ages out.
        {
            vector<uint8_t> respBuf;
            if (sendChunk(0xC0DE0003ull, 0, split, respBuf) < 0) {
                cerr << "FAIL: chunk 16c first chunk rejected" << endl; return 1;
            }
            // Starts one byte before the first chunk ends -> overlap.
            if (sendChunk(0xC0DE0003ull, split - 1, total - split + 1, respBuf) >= 0) {
                cerr << "FAIL: chunk 16c overlapping chunk was ACCEPTED; the completion contract lets a zero-fill hole through" << endl;
                return 1;
            }
            // The id is poisoned: neither a mid-stream continuation nor a
            // from-scratch re-open may be acked.
            if (sendChunk(0xC0DE0003ull, split, total - split, respBuf) >= 0) {
                cerr << "FAIL: chunk 16c continuation after rejection was ACCEPTED; the rejected transfer was resurrected" << endl;
                return 1;
            }
            if (sendChunk(0xC0DE0003ull, 0, split, respBuf) >= 0) {
                cerr << "FAIL: chunk 16c re-open after rejection was ACCEPTED; the transfer id is not poisoned" << endl;
                return 1;
            }
        }

        // 16d. The poison is PER ID, not global: a fresh transfer id must still
        // complete normally right after another id was rejected. (Also proves
        // 16c's rejections left no reassembly state behind for case 17.)
        {
            vector<uint8_t> respBuf;
            if (sendChunk(0xC0DE0004ull, 0, split, respBuf) < 0) {
                cerr << "FAIL: chunk 16d first chunk rejected (poison leaked across transfer ids?)" << endl; return 1;
            }
            if (sendChunk(0xC0DE0004ull, split, total - split, respBuf) < 0) {
                cerr << "FAIL: chunk 16d final chunk rejected" << endl; return 1;
            }
            auto resp = flatbuffers::GetRoot<ipc::EchoStringResponse>(respBuf.data());
            ASSERT_STREQ(echoVal, resp->result()->str(), "Chunked EchoString on a fresh id after a rejection");
        }

        // 16e. A present-but-EMPTY data vector is refused. It advances nothing,
        // so it can never be part of a valid transfer — and if it were recorded
        // as an (offset, 0) segment, the REAL chunk arriving at that offset
        // would classify as "same offset, different length" => overlap => the
        // whole healthy transfer discarded. One stray empty frame must not be
        // able to kill a good transfer.
        {
            vector<uint8_t> respBuf;
            if (sendChunk(0xC0DE0005ull, 0, 0, respBuf) >= 0) {
                cerr << "FAIL: chunk 16e zero-length segment was ACCEPTED; it would turn the real chunk at that offset into an overlap" << endl;
                return 1;
            }
        }

        // 17. Concurrent-transfer bound (server.DefaultMaxConcurrentTransfers,
        // 1024). Open that many transfers without finishing any of them, then
        // assert the next one is refused. The per-transfer byte cap does not
        // bound the aggregate; this does.
        //
        // Every case above either completed or was dropped, so the guest starts
        // this block with an empty reassembly table — if any leftover existed,
        // one of the first 1024 would be refused and the test would say so.
        // Poison entries live in their own map and do not consume slots, which
        // 16c/16e implicitly confirm here.
        {
            const int kMaxConcurrent = 1024;
            vector<uint8_t> respBuf;
            for (int i = 0; i < kMaxConcurrent; ++i) {
                uint64_t id = 0xC0DE1000ull + (uint64_t)i;
                if (sendChunk(id, 0, split, respBuf) < 0) {
                    cerr << "FAIL: transfer " << i << " of " << kMaxConcurrent
                         << " refused before the bound was reached" << endl;
                    return 1;
                }
            }
            if (sendChunk(0xC0DEFFFFull, 0, split, respBuf) >= 0) {
                cerr << "FAIL: transfer " << (kMaxConcurrent + 1)
                     << " was ACCEPTED; the concurrent-transfer bound is not enforced" << endl;
                return 1;
            }
        }
    }

    cout << "PASSED" << endl;
    return 0;
}
