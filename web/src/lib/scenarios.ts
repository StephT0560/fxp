// SPDX-License-Identifier: MIT
import type { FXPMessage } from "../gen/fxp_pb";
import { dispatch } from "./client";
import { useOrders } from "./stores";

/** Feed observed server-side sessions into the existing console stores. */
export function applyScenarioMessage(msg: FXPMessage): void {
  if (msg.payload.case === "orderStatusResponse") {
    for (const report of msg.payload.value.reports) {
      // Status ExecID=0 is not an execution identity. Bypass event deduplication
      // for each retained status row without replacing the viewer's own blotter.
      useOrders.getState().onReport({ ...report, executionId: "" });
    }
  } else dispatch(msg);
}
