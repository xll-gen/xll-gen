#include <iostream>
#include <vector>
#include <thread>
#include <chrono>
#include <cmath>
#include "shm/DirectHost.h"
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
int main() {
    shm::DirectHost host;
    if (!host.Init("smoke_proj", 1024, 1024*1024, 16)) {
        cerr << "Failed to init SHM" << endl;
        return 1;
    }
    cout << "READY" << endl;

    flatbuffers::FlatBufferBuilder builder(1024);

    // 1. EchoInt (ID 132)
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
                sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 132, respBuf);
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
             sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 132, respBuf);
        }

        if (sz < 0) { cerr << "Send failed for EchoInt " << val << endl; return 1; }

        auto resp = flatbuffers::GetRoot<ipc::EchoIntResponse>(respBuf.data());
        if (resp->error() && resp->error()->size() > 0) { cerr << "Error: " << resp->error()->str() << endl; return 1; }
        ASSERT_EQ(val, resp->result(), "EchoInt");
    }

    // 2. EchoFloat (ID 133)
    vector<double> floatCases = {0.0, 1.5, -999.99};
    for (auto val : floatCases) {
        builder.Reset();
        ipc::EchoFloatRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 133, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatResponse>(respBuf.data());
        if (std::abs(val - resp->result()) > 0.0001) { cerr << "Float mismatch" << endl; return 1; }
    }

    // 3. EchoString (ID 134)
    vector<string> strCases = {"test", "", "Hello World"};
    for (auto val : strCases) {
        builder.Reset();
        auto off = builder.CreateString(val);
        ipc::EchoStringRequestBuilder req(builder);
        req.add_val(off);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 134, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringResponse>(respBuf.data());
        ASSERT_STREQ(val, resp->result()->str(), "EchoString");
    }

    // 4. EchoBool (ID 135)
    vector<bool> boolCases = {true, false};
    for (auto val : boolCases) {
        builder.Reset();
        ipc::EchoBoolRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 135, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolResponse>(respBuf.data());
        ASSERT_EQ(val, resp->result(), "EchoBool");
    }

    // 9. AsyncEchoInt (ID 136)
    {
        builder.Reset();
        ipc::AsyncEchoIntRequestBuilder req(builder);
        req.add_val(42);
        req.add_async_handle(999);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 136, respBuf);
        if (sz < 0) return 1;

        // Async returns immediately with void

        // Wait for callback
        bool gotCallback = false;
        auto start = chrono::steady_clock::now();
        int spin = 0;
        while(chrono::steady_clock::now() - start < chrono::seconds(2)) {
<<<<<<< HEAD
            int n = host.ProcessGuestCalls([&](const uint8_t* req, uint32_t msgType, uint8_t* resp, uint32_t size, uint32_t timeoutMs) -> int32_t {
                // Check MsgType = 139
                if (msgType == 139) {
=======
            int n = host.ProcessGuestCalls([&](const uint8_t* req, int32_t size, uint8_t* resp, uint32_t capacity, uint32_t msgId) -> int32_t {
                // Check MsgID = 136
                if (msgId == 136) {
>>>>>>> origin/main
                     auto response = flatbuffers::GetRoot<ipc::AsyncEchoIntResponse>(req);
                     if (response->async_handle() == 999 && response->result() == 42) {
                         gotCallback = true;
                     }
                }
                return 0;
            });
            if (gotCallback) break;

            if (n == 0) {
                if (spin < 1000) {
                    this_thread::yield();
                    spin++;
                } else {
                    this_thread::sleep_for(chrono::milliseconds(1));
                    spin = 0;
                }
            } else {
                spin = 0;
            }
        }
        if (!gotCallback) { cerr << "Async callback missing" << endl; return 1; }
    }

    // 10. CheckAny (ID 137)
    // Int
    {
        builder.Reset();
        auto val = ipc::types::CreateInt(builder, 10);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Int, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 137, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Int:10", resp->result()->str(), "CheckAny Int");
    }
    // Str
    {
        builder.Reset();
        auto s = builder.CreateString("hello");
        auto val = ipc::types::CreateStr(builder, s);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Str, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 137, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Str:hello", resp->result()->str(), "CheckAny Str");
    }
    // Num
    {
        builder.Reset();
        auto val = ipc::types::CreateNum(builder, 1.5);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Num, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 137, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Num:1.5", resp->result()->str(), "CheckAny Num");
    }

    // NumGrid
    {
        builder.Reset();
        std::vector<double> data = {1.1, 2.2};
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateNumGrid(builder, 1, 2, dataOff);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_NumGrid, arr.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 137, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("NumGrid:1x2", resp->result()->str(), "CheckAny NumGrid");
    }

    // Grid
    {
        builder.Reset();
        auto val1 = ipc::types::CreateInt(builder, 1);
        auto s1 = ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Int, val1.Union());
        auto val2 = ipc::types::CreateBool(builder, true);
        auto s2 = ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Bool, val2.Union());

        std::vector<flatbuffers::Offset<ipc::types::Scalar>> data = {s1, s2};
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateGrid(builder, 1, 2, dataOff);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Grid, arr.Union());

        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 137, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Grid:1x2", resp->result()->str(), "CheckAny Grid");
    }

    // 11. CheckRange (ID 138)
    {
        builder.Reset();
        auto sOff = builder.CreateString("Sheet1");
        std::vector<ipc::types::Rect> refs = { {1,1,1,1} };
        auto refsOff = builder.CreateVectorOfStructs(refs);
        auto rangeVal = ipc::types::CreateRange(builder, sOff, refsOff);
        ipc::CheckRangeRequestBuilder req(builder);
        req.add_val(rangeVal);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 138, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckRangeResponse>(respBuf.data());
        ASSERT_STREQ("Range:Sheet1!1:1:1:1", resp->result()->str(), "CheckRange");
    }

    // 12. TimeoutFunc (ID 139)
    {
        builder.Reset();
        ipc::TimeoutFuncRequestBuilder req(builder);
        req.add_val(10);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 139, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::TimeoutFuncResponse>(respBuf.data());

        // Timeout now returns -1 instead of error
        ASSERT_EQ(-1, resp->result(), "TimeoutFunc");
    }

    // 13. CalculationEnded Commands - Set (ID 140)
    {
        // 1. Call ScheduleCmd (ID 140)
        builder.Reset();
        ipc::ScheduleCmdRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 140, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleCmdResponse>(respBuf.data());
        ASSERT_EQ(1, resp->result(), "ScheduleCmd");

        // 2. Send CalculationEnded (ID 130)
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, 130, eventBuf) < 0) return 1;

        // 3. Verify Response contains SetCommand
        if (eventBuf.empty()) { cerr << "Expected commands in CalcEnded response" << endl; return 1; }
        auto eventResp = flatbuffers::GetRoot<ipc::CalculationEndedResponse>(eventBuf.data());

        if (!eventResp->commands()) { cerr << "No commands list" << endl; return 1; }
        if (eventResp->commands()->size() != 1) { cerr << "Expected 1 command" << endl; return 1; }

        auto wrapper = eventResp->commands()->Get(0);
        if (wrapper->cmd_type() != ipc::Command_SetCommand) { cerr << "Expected SetCommand" << endl; return 1; }

        auto setCmd = static_cast<const ipc::SetCommand*>(wrapper->cmd());
        auto rng = setCmd->target();
        ASSERT_STREQ("Sheet1", rng->sheet_name()->str(), "SetCommand Sheet");

        auto val = setCmd->value();
        ASSERT_EQ(ipc::types::AnyValue_Int, val->val_type(), "SetCommand ValType");
        ASSERT_EQ(100, val->val_as_Int()->val(), "SetCommand Val");
    }

    // 14. CalculationEnded Commands - Format (ID 141)
    {
        // 1. Call ScheduleFormatCmd (ID 141)
        builder.Reset();
        ipc::ScheduleFormatCmdRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 141, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleFormatCmdResponse>(respBuf.data());
        ASSERT_EQ(1, resp->result(), "ScheduleFormatCmd");

        // 2. Send CalculationEnded
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, 130, eventBuf) < 0) return 1;

        // 3. Verify Response contains FormatCommand
        auto eventResp = flatbuffers::GetRoot<ipc::CalculationEndedResponse>(eventBuf.data());
        if (eventResp->commands()->size() != 1) { cerr << "Expected 1 format command" << endl; return 1; }

        auto wrapper = eventResp->commands()->Get(0);
        if (wrapper->cmd_type() != ipc::Command_FormatCommand) { cerr << "Expected FormatCommand" << endl; return 1; }

        auto fmtCmd = static_cast<const ipc::FormatCommand*>(wrapper->cmd());
        auto rng = fmtCmd->target();
        ASSERT_STREQ("Sheet1", rng->sheet_name()->str(), "FormatCommand Sheet");

        ASSERT_STREQ("General", fmtCmd->format()->str(), "FormatCommand Format");
    }

    // 15. CalculationEnded Commands - Multi (ID 142)
    {
        // 1. Call ScheduleMultiCmd (ID 142)
        builder.Reset();
        ipc::ScheduleMultiCmdRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 142, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleMultiCmdResponse>(respBuf.data());
        ASSERT_EQ(2, resp->result(), "ScheduleMultiCmd");

        // 2. Send CalculationEnded
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, 130, eventBuf) < 0) return 1;

        // 3. Verify Response contains 2 commands (Set, Format)
        auto eventResp = flatbuffers::GetRoot<ipc::CalculationEndedResponse>(eventBuf.data());
        if (eventResp->commands()->size() != 2) { cerr << "Expected 2 commands" << endl; return 1; }

        // First: Set
        {
            auto wrapper = eventResp->commands()->Get(0);
            if (wrapper->cmd_type() != ipc::Command_SetCommand) { cerr << "Expected SetCommand 1st" << endl; return 1; }
            auto setCmd = static_cast<const ipc::SetCommand*>(wrapper->cmd());
            auto val = setCmd->value();
            ASSERT_EQ(200, val->val_as_Int()->val(), "Multi SetCommand Val");
        }
        // Second: Format
        {
            auto wrapper = eventResp->commands()->Get(1);
            if (wrapper->cmd_type() != ipc::Command_FormatCommand) { cerr << "Expected FormatCommand 2nd" << endl; return 1; }
            auto fmtCmd = static_cast<const ipc::FormatCommand*>(wrapper->cmd());
            ASSERT_STREQ("Number", fmtCmd->format()->str(), "Multi FormatCommand Format");
        }
    }

    // 16. ScheduleMassive (ID 143)
    {
        // 1. Call ScheduleMassive
        builder.Reset();
        ipc::ScheduleMassiveRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 143, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::ScheduleMassiveResponse>(respBuf.data());
        ASSERT_EQ(100, resp->result(), "ScheduleMassive");

        // 2. Send CalculationEnded
        vector<uint8_t> eventBuf;
        if(host.Send(nullptr, 0, 130, eventBuf) < 0) return 1;

        // 3. Verify Response
        auto eventResp = flatbuffers::GetRoot<ipc::CalculationEndedResponse>(eventBuf.data());
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
             if (wrapper->cmd_type() == ipc::Command_SetCommand) {
                 auto setCmd = static_cast<const ipc::SetCommand*>(wrapper->cmd());
                 auto val = setCmd->value()->val_as_Int()->val();
                 if (val == 100) count100++;
                 else if (val == 200) count200++;
             }
        }
        ASSERT_EQ(2, count100, "Count 100 commands");
        ASSERT_EQ(2, count200, "Count 200 commands");
    }

    cout << "PASSED" << endl;
    return 0;
}
