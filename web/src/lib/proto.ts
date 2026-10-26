// SPDX-License-Identifier: MIT
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { timestampNow } from "@bufbuild/protobuf/wkt";
import {
  FXPMessageSchema,
  LogonSchema,
  MarketDataRequestSchema,
  NewOrderSingleSchema,
  OrderCancelReplaceRequestSchema,
  OrderCancelRequestSchema,
  OrderMassCancelRequestSchema,
  OrderStatusRequestSchema,
  type FXPMessage,
} from "../gen/fxp_pb";
import type { OrderDraft } from "./validate";

export function decodeFrame(data: ArrayBuffer | Uint8Array): FXPMessage {
  return fromBinary(FXPMessageSchema, data instanceof Uint8Array ? data : new Uint8Array(data));
}

export function logonFrame(token: string, sessionId: string): Uint8Array {
  return toBinary(
    FXPMessageSchema,
    create(FXPMessageSchema, {
      payload: { case: "logon", value: create(LogonSchema, { token, sessionId, timestamp: timestampNow() }) },
    }),
  );
}

export function subscribeFrame(symbols: string[]): Uint8Array {
  return toBinary(
    FXPMessageSchema,
    create(FXPMessageSchema, {
      payload: { case: "marketDataRequest", value: create(MarketDataRequestSchema, { symbols }) },
    }),
  );
}

const encode = (payload: FXPMessage["payload"]) => toBinary(FXPMessageSchema, create(FXPMessageSchema, { payload }));

export function newOrderFrame(orderId: string, d: OrderDraft): Uint8Array {
  return encode({
    case: "newOrder",
    value: create(NewOrderSingleSchema, {
      orderId,
      clientOrderId: orderId,
      symbol: d.symbol,
      side: d.side,
      orderType: d.type,
      timeInForce: d.tif,
      price: d.price,
      stopPrice: d.stopPrice,
      quantity: d.qty,
      displayQuantity: d.displayQty,
      expireDate: d.expireDate,
      stpMode: d.stpMode,
      stpGroup: d.stpGroup,
      timestamp: timestampNow(),
    }),
  });
}

export function cancelFrame(orderId: string, symbol: string, requestId: string): Uint8Array {
  return encode({
    case: "orderCancel",
    value: create(OrderCancelRequestSchema, { orderId, symbol, clientCancelId: requestId, timestamp: timestampNow() }),
  });
}

export interface ReplaceChange {
  price?: bigint;
  qty?: number;
  displayQty?: number;
}

export function replaceFrame(orderId: string, newOrderId: string, symbol: string, change: ReplaceChange): Uint8Array {
  return encode({
    case: "orderCancelReplace",
    value: create(OrderCancelReplaceRequestSchema, {
      orderId,
      newOrderId,
      clientReplaceId: newOrderId,
      symbol,
      newPrice: change.price ?? 0n,
      newQuantity: change.qty ?? 0,
      newDisplayQuantity: change.displayQty,
      timestamp: timestampNow(),
    }),
  });
}

export function massCancelFrame(requestId: string, symbol = ""): Uint8Array {
  return encode({
    case: "orderMassCancel",
    value: create(OrderMassCancelRequestSchema, { clientRequestId: requestId, symbol, timestamp: timestampNow() }),
  });
}

export function statusRequestFrame(requestId: string): Uint8Array {
  return encode({ case: "orderStatusRequest", value: create(OrderStatusRequestSchema, { requestId }) });
}
