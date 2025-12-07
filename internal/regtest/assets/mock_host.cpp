
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

int main() {
    shm::DirectHost host;
    if (!host.Init("smoke_proj", 1024, 1024*1024, 16)) {
        cerr << "Failed to init SHM" << endl;
        return 1;
    }
    cout << "READY" << endl;

    flatbuffers::FlatBufferBuilder builder(1024);

    // 1. EchoInt (ID 11)
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
             while(chrono::steady_clock::now() - startWait < chrono::seconds(30)) {
                sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 11, respBuf);
                if (sz >= 0) break;
                this_thread::sleep_for(chrono::milliseconds(100));
             }
        } else {
             sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 11, respBuf);
        }

        if (sz < 0) { cerr << "Send failed for EchoInt " << val << endl; return 1; }

        auto resp = flatbuffers::GetRoot<ipc::EchoIntResponse>(respBuf.data());
        if (resp->error() && resp->error()->size() > 0) { cerr << "Error: " << resp->error()->str() << endl; return 1; }
        ASSERT_EQ(val, resp->result(), "EchoInt");
    }

    // 2. EchoFloat (ID 12)
    vector<double> floatCases = {0.0, 1.5, -999.99};
    for (auto val : floatCases) {
        builder.Reset();
        ipc::EchoFloatRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 12, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatResponse>(respBuf.data());
        if (std::abs(val - resp->result()) > 0.0001) { cerr << "Float mismatch" << endl; return 1; }
    }

    // 3. EchoString (ID 13)
    vector<string> strCases = {"test", "", "Hello World"};
    for (auto val : strCases) {
        builder.Reset();
        auto off = builder.CreateString(val);
        ipc::EchoStringRequestBuilder req(builder);
        req.add_val(off);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 13, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringResponse>(respBuf.data());
        ASSERT_STREQ(val, resp->result()->str(), "EchoString");
    }

    // 4. EchoBool (ID 14)
    vector<bool> boolCases = {true, false};
    for (auto val : boolCases) {
        builder.Reset();
        ipc::EchoBoolRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 14, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolResponse>(respBuf.data());
        ASSERT_EQ(val, resp->result(), "EchoBool");
    }

    // 5. EchoIntOpt (ID 15)
    // Case: Value
    {
        builder.Reset();
        auto valOff = ipc::types::CreateInt(builder, 123);
        ipc::EchoIntOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 15, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoIntOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        ASSERT_EQ(123, resp->result()->val(), "EchoIntOpt Val");
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoIntOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 15, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoIntOptResponse>(respBuf.data());
        if (resp->result()) { cerr << "Expected null" << endl; return 1; }
    }

    // 6. EchoFloatOpt (ID 16)
    // Case: Value
    {
        builder.Reset();
        auto valOff = ipc::types::CreateNum(builder, 3.14);
        ipc::EchoFloatOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 16, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        if (std::abs(resp->result()->val() - 3.14) > 0.001) { cerr << "FloatOpt mismatch" << endl; return 1; }
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoFloatOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 16, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatOptResponse>(respBuf.data());
        if (resp->result()) { cerr << "Expected null" << endl; return 1; }
    }

    // 7. EchoStringOpt (ID 17)
    // Case: Value
    {
        builder.Reset();
        auto sOff = builder.CreateString("opt");
        auto valOff = ipc::types::CreateStr(builder, sOff);
        ipc::EchoStringOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 17, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        ASSERT_STREQ("opt", resp->result()->val()->str(), "EchoStringOpt Val");
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoStringOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 17, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringOptResponse>(respBuf.data());
        if (resp->result()) { cerr << "Expected null" << endl; return 1; }
    }

    // 8. EchoBoolOpt (ID 18)
    // Case: Value
    {
        builder.Reset();
        auto valOff = ipc::types::CreateBool(builder, true);
        ipc::EchoBoolOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 18, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        ASSERT_EQ(true, resp->result()->val(), "EchoBoolOpt Val");
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoBoolOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 18, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolOptResponse>(respBuf.data());
        if (resp->result()) { cerr << "Expected null" << endl; return 1; }
    }

    // 9. AsyncEchoInt (ID 19)
    {
        builder.Reset();
        ipc::AsyncEchoIntRequestBuilder req(builder);
        req.add_val(42);
        req.add_async_handle(999);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 19, respBuf);
        if (sz < 0) return 1;

        // Async returns immediately with void

        // Wait for callback
        bool gotCallback = false;
        auto start = chrono::steady_clock::now();
        while(chrono::steady_clock::now() - start < chrono::seconds(2)) {
            host.ProcessGuestCalls([&](const uint8_t* req, int32_t size, uint8_t* resp, uint32_t msgId) -> int32_t {
                // Check MsgID = 19
                if (msgId == 19) {
                     auto response = flatbuffers::GetRoot<ipc::AsyncEchoIntResponse>(req);
                     if (response->async_handle() == 999 && response->result() == 42) {
                         gotCallback = true;
                     }
                }
                return 0;
            });
            if (gotCallback) break;
            this_thread::sleep_for(chrono::milliseconds(10));
        }
        if (!gotCallback) { cerr << "Async callback missing" << endl; return 1; }
    }

    // 10. CheckAny (ID 20)
    // Int
    {
        builder.Reset();
        auto val = ipc::types::CreateInt(builder, 10);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Int, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
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
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
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
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Num:1.5", resp->result()->str(), "CheckAny Num");
    }

    // NumArray
    {
        builder.Reset();
        std::vector<double> data = {1.1, 2.2};
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateNumArray(builder, 1, 2, dataOff);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_NumArray, arr.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("NumArray:1x2", resp->result()->str(), "CheckAny NumArray");
    }

    // Array
    {
        builder.Reset();
        auto val1 = ipc::types::CreateInt(builder, 1);
        auto s1 = ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Int, val1.Union());
        auto val2 = ipc::types::CreateBool(builder, true);
        auto s2 = ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Bool, val2.Union());

        std::vector<flatbuffers::Offset<ipc::types::Scalar>> data = {s1, s2};
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateArray(builder, 1, 2, dataOff);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Array, arr.Union());

        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Array:1x2", resp->result()->str(), "CheckAny Array");
    }

    // 11. CheckRange (ID 21)
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
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 21, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckRangeResponse>(respBuf.data());
        ASSERT_STREQ("Range:Sheet1!1:1:1:1", resp->result()->str(), "CheckRange");
    }

    // 12. TimeoutFunc (ID 22)
    {
        builder.Reset();
        ipc::TimeoutFuncRequestBuilder req(builder);
        req.add_val(10);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 22, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::TimeoutFuncResponse>(respBuf.data());

        // Timeout now returns -1 instead of error
        ASSERT_EQ(-1, resp->result(), "TimeoutFunc");
    }

    cout << "PASSED" << endl;
    return 0;
}
