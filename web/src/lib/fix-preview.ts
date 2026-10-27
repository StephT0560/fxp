// SPDX-License-Identifier: MIT
const names: Record<string, string> = {
  "8": "BeginString", "9": "BodyLength", "10": "CheckSum", "11": "ClOrdID",
  "34": "MsgSeqNum", "35": "MsgType", "38": "OrderQty", "40": "OrdType",
  "44": "Price", "49": "SenderCompID", "52": "SendingTime", "54": "Side",
  "55": "Symbol", "56": "TargetCompID", "59": "TimeInForce", "60": "TransactTime",
  "99": "StopPx", "111": "MaxFloor", "432": "ExpireDate", "1": "Account",
  "2362": "SelfMatchPreventionID", "2964": "SelfMatchPreventionInstruction",
};

/** Presentation only: preserve order, unknown tags and empty values; never translate orders. */
export function parseFixPreview(preview: string) {
  return preview.split("|").filter((field) => field.length > 0).map((field) => {
    const separator = field.indexOf("=");
    const tag = separator < 0 ? field : field.slice(0, separator);
    return { tag, name: names[tag] ?? "", value: separator < 0 ? "" : field.slice(separator + 1) };
  });
}
