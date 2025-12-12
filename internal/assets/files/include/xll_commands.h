#pragma once

#include "schema_generated.h"
#include "include/xlcall.h"

// Check if an XLOPER12 reference represents a single cell
bool IsSingleCell(LPXLOPER12 xRef);

// Execute a batch of commands (Set, Format) received from the server
void ExecuteCommands(const ipc::CalculationEndedResponse* resp);
