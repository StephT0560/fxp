// SPDX-License-Identifier: MIT
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { timestampNow } from "@bufbuild/protobuf/wkt";
import { FXPMessageSchema, LogonSchema, MarketDataRequestSchema, type FXPMessage } from "../gen/fxp_pb";

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
