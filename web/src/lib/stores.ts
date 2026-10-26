// SPDX-License-Identifier: MIT
import { create } from "zustand";
import type {
  AuctionInfo,
  MarketDataIncrementalUpdate,
  MarketDataSnapshot,
  TradeCaptureReport,
  TradingPhase,
} from "../gen/fxp_pb";
import { applyIncremental, applySnapshot, emptyBook, type Book } from "./book";
import { schedule } from "./scheduler";
import { applyTrade, emptyTape, type Tape } from "./tape";

export type ConnState = "idle" | "connecting" | "live" | "reconnecting" | "rejected";

export interface SessionState {
  user?: string;
  conn: ConnState;
  attempt: number;
  error?: string;
  phases: Record<string, TradingPhase>;
  auctions: Record<string, AuctionInfo>;
  setConn: (conn: ConnState, error?: string, attempt?: number) => void;
  setUser: (user?: string) => void;
  onStatus: (symbol: string, phase: TradingPhase) => void;
  onAuction: (a: AuctionInfo) => void;
}

export const useSession = create<SessionState>((set) => ({
  conn: "idle",
  attempt: 0,
  phases: {},
  auctions: {},
  setConn: (conn, error, attempt = 0) => set({ conn, error, attempt }),
  setUser: (user) => set({ user }),
  onStatus: (symbol, phase) => set((s) => ({ phases: { ...s.phases, [symbol]: phase } })),
  onAuction: (a) => set((s) => ({ auctions: { ...s.auctions, [a.symbol]: a } })),
}));

export interface SymbolInfo {
  symbol: string;
  price_scale: number;
  tick_size: string;
}

export interface SymbolsState {
  list: SymbolInfo[];
  selected?: string;
  setList: (list: SymbolInfo[]) => void;
  select: (symbol: string) => void;
}

export const useSymbols = create<SymbolsState>((set) => ({
  list: [],
  setList: (list) => set((s) => ({ list, selected: s.selected ?? list[0]?.symbol })),
  select: (selected) => set({ selected }),
}));

/** Books are mutated in place; `version` bumps at most once per frame. */
export interface BookState {
  books: Map<string, Book>;
  version: number;
  dropped: number;
  onSnapshot: (s: MarketDataSnapshot) => void;
  onIncremental: (u: MarketDataIncrementalUpdate) => void;
  unsync: () => void;
}

export const useBook = create<BookState>((set, get) => {
  const bump = () => set((s) => ({ version: s.version + 1 }));
  const book = (symbol: string) => {
    const books = get().books;
    let b = books.get(symbol);
    if (!b) books.set(symbol, (b = emptyBook(symbol)));
    return b;
  };
  return {
    books: new Map(),
    version: 0,
    dropped: 0,
    onSnapshot: (s) => {
      applySnapshot(book(s.symbol), s);
      schedule(bump);
    },
    onIncremental: (u) => {
      if (!applyIncremental(book(u.symbol), u)) get().dropped++;
      schedule(bump);
    },
    unsync: () => {
      for (const b of get().books.values()) b.synced = false;
      schedule(bump);
    },
  };
});

export interface TapeState {
  tapes: Map<string, Tape>;
  version: number;
  onTrade: (t: TradeCaptureReport) => void;
}

export const useTape = create<TapeState>((set, get) => {
  const bump = () => set((s) => ({ version: s.version + 1 }));
  return {
    tapes: new Map(),
    version: 0,
    onTrade: (t) => {
      const tapes = get().tapes;
      let tape = tapes.get(t.symbol);
      if (!tape) tapes.set(t.symbol, (tape = emptyTape()));
      applyTrade(tape, t);
      schedule(bump);
    },
  };
});
