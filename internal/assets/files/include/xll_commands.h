#pragma once

#include "protocol_generated.h"
#include "include/xlcall.h"

// Execute a batch of commands (Set, Format) received from the server
void ExecuteCommands(const flatbuffers::Vector<flatbuffers::Offset<protocol::CommandWrapper>>* commands);
