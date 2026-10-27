// SPDX-License-Identifier: MIT
import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import {
  FXPMessageSchema,
  ExecutionReportSchema,
  ExecutionType,
  OrderStatus,
} from "../gen/fxp_pb";
import { useOrders } from "./stores";
import { applyScenarioMessage } from "./scenarios";

it("applies both retained COD status rows even though status ExecID is always zero", () => {
  useOrders.getState().reset();
  const reports = ["cod-first", "cod-second"].map((orderId) =>
    create(ExecutionReportSchema, {
      executionId: "0",
      orderId,
      symbol: "SCN-test",
      executionType: ExecutionType.EXEC_ORDER_STATUS,
      orderStatus: OrderStatus.ORDER_CANCELED,
      orderQuantity: 7,
      leavesQuantity: 0,
    }),
  );
  for (const report of reports)
    applyScenarioMessage(
      create(FXPMessageSchema, {
        payload: {
          case: "orderStatusResponse",
          value: {
            requestId: report.orderId,
            sender: "observer",
            reports: [report],
            rejectReason: "",
            complete: true,
            totalReports: 1,
          },
        },
      }),
    );
  expect(useOrders.getState().blotter.rows.get("cod-first")?.state).toBe(
    "canceled",
  );
  expect(useOrders.getState().blotter.rows.get("cod-second")?.state).toBe(
    "canceled",
  );
});
