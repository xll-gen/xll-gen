#pragma once
#include "shm/DirectHost.h"

namespace xll {
    // Starts the worker loop that listens for messages from the guest.
    // It handles MSG_CHUNK and MSG_BATCH_ASYNC_RESPONSE internally.
    void StartWorker(shm::DirectHost& host);
}
