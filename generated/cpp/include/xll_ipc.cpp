#include "xll_ipc.h"
#include "schema_generated.h"
#include <random>
#include <thread>
#include <iostream>

// Global definitions
shm::DirectHost g_host;
std::map<std::string, bool> g_sentRefCache;
std::mutex g_refCacheMutex;
