# FXP Console: three-beat demo (~75 seconds)

One recorded window, logged in as **alice**. A separate, unrecorded **bob**
context supplies the counterparty orders. Run the [recording harness](recording.md)
after `make demo`; it performs these real actions and assertions, without mocked
market data. The [extended walkthrough](demo-script-extended.md) retains the
longer two-window tour, including rejects, cancel-all and reconnect.

| Time | Action | Narration |
|---|---|---|
| 0:00–0:20 | Log in as Alice, select **DEMO**. Start seed **42**, ten-second opening auction. Bob sells 25 @ 60.00 **At open**; Alice buys the same. Show the countdown, then the **AUC** tape print at the uncross. | “Orders queue during the opening call. The countdown is an estimate from observed phase arrival. At the uncross, the core matches them and publishes an auction print.” |
| 0:20–0:48 | Stop the simulator after the uncross. Bob rests a sell for 40 @ 50.00 GTC. Alice buys 100 @ 50.00 GTC: show the tape print, **cum 40 / leaves 60**, and Alice’s ladder marker. Replace Alice’s remainder to **50.10**, keeping total quantity 100. | “Alice crosses Bob’s resting order. Forty fill; sixty remain. Replace moves the remainder to a new price and loses its previous queue priority. The blue marker identifies our locally known resting quantity.” |
| 0:48–1:15 | Open **Protocol inspector**, filter sent `newOrder` / `DEMO`, select Alice’s 100-lot order, then **FXP vs FIX for this order**. Show decoded fields, raw hex and byte counts. Switch to **Latency**, show both ACK series, repetition spread and environment. | “These are the same order’s FXP bytes and authoritative FIX 4.4 translation from fix_bridge, with SOH displayed as a pipe. Byte counts are a fixture comparison, not a speed ranking. Recorded latency separates intended-time ACK from post-send ACK and shows min/max across repetitions. Plaintext loopback only: a tested knee, not an SLO, and no WAN or cross-protocol ranking claim.” |

The `DEMO` book is configured but never traded by the simulator. The harness
uses two 25-lot at-open orders for a predictable auction print, then a 40/100
continuous cross for the partial fill. A fixed seed controls simulator random
choices; live scheduling, timestamps and generated order IDs still vary between
takes. The recording has no audio; the narration above is a voiceover guide.
