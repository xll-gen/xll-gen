#include <iostream>
#include <vector>
#include <thread>
#include <chrono>
#include <cmath>
#include "shm/DirectHost.h"
#include "protocol_generated.h"
#include "schema_generated.h"

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

    // 13. RefCache Cleanup on Canceled (ID 130, 132, 144)
    {
        // 1. Send SetRefCache "K1" = Int(123)
        builder.Reset();
        auto keyOff = builder.CreateString("K1");
        auto valOff = protocol::CreateInt(builder, 123);
        auto anyOff = protocol::CreateAny(builder, protocol::AnyValue::Int, valOff.Union());
        auto req = protocol::CreateSetRefCacheRequest(builder, keyOff, anyOff);
        builder.Finish(req);
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)130, respBuf).ValueOr(-1) < 0) return 1;
        // Expect Ack
        auto ack = flatbuffers::GetRoot<protocol::Ack>(respBuf.data());
        ASSERT_EQ(true, ack->ok(), "SetRefCache Ack");

        // 2. Send CheckAny with RefCache("K1") -> Expect "Int:123"
        builder.Reset();
        keyOff = builder.CreateString("K1");
        auto rcVal = protocol::CreateRefCache(builder, keyOff);
        anyOff = protocol::CreateAny(builder, protocol::AnyValue::RefCache, rcVal.Union());
        ipc::CheckAnyRequestBuilder caReq(builder);
        caReq.add_val(anyOff);
        builder.Finish(caReq.Finish());
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1) < 0) return 1;
        auto caResp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Int:123", caResp->result()->str(), "CheckAny RefCache Resolved");

        // 3. Send CalculationCanceled (ID 132)
        if(host.Send(nullptr, 0, (shm::MsgType)132, respBuf).ValueOr(-1) < 0) return 1;
        // No response payload expected (size 0)

        // 4. Verify RefCache persists after Canceled (since we reverted the cleanup)
        // Send CheckAny with RefCache("K1") -> Expect "Int:123"
        builder.Reset();
        keyOff = builder.CreateString("K1");
        rcVal = protocol::CreateRefCache(builder, keyOff);
        anyOff = protocol::CreateAny(builder, protocol::AnyValue::RefCache, rcVal.Union());
        ipc::CheckAnyRequestBuilder caReq2(builder);
        caReq2.add_val(anyOff);
        builder.Finish(caReq2.Finish());
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1) < 0) return 1;
        caResp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Int:123", caResp->result()->str(), "CheckAny RefCache Persists");

        // 5. Send CalculationEnded (ID 131)
        if(host.Send(nullptr, 0, (shm::MsgType)131, respBuf).ValueOr(-1) < 0) return 1;

        // 6. Verify RefCache Cleared after Ended
        // Send CheckAny with RefCache("K1") -> Expect "RefCache:K1"
        builder.Reset();
        keyOff = builder.CreateString("K1");
        rcVal = protocol::CreateRefCache(builder, keyOff);
        anyOff = protocol::CreateAny(builder, protocol::AnyValue::RefCache, rcVal.Union());
        ipc::CheckAnyRequestBuilder caReq3(builder);
        caReq3.add_val(anyOff);
        builder.Finish(caReq3.Finish());
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), (shm::MsgType)144, respBuf).ValueOr(-1) < 0) return 1;
        caResp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("RefCache:K1", caResp->result()->str(), "CheckAny RefCache Cleared");
    }

    cout << "PASSED" << endl;
    return 0;
}
