# FXP Console: 90-second demo script

The walkthrough for the README recording. It covers the v0.1 success path
(spec §10) with no terminal after `make demo`. Use two browser windows side by
side: **A** logged in as `alice`, **B** as `bob`.

| Time | Action | What to show |
|---|---|---|
| 0:00 | Open http://127.0.0.1:3000 in A, pick **alice**. | Status bar turns **live**; symbol list and empty ladder. |
| 0:05 | Select **AAPL**. Leave **open with auction** checked (20 s), press **Start simulator**. | Phase shows **Opening call**; the amber auction banner shows the indicative price, volume and imbalance moving as orders arrive. |
| 0:15 | In B, pick **bob**, select **DEMO**. In A, select **DEMO** too. | DEMO is in the call as well; the simulator never trades it, so your orders are the only ones. |
| 0:20 | A: Buy 25 @ 60.00, time in force **At open**. B: Sell 25 @ 60.00, **At open**. | Both blotters show the orders working; nothing fills during the call. |
| 0:30 | Wait for the uncross. Switch A to AAPL. | Phase goes to **Continuous**; the tape fills with **AUC** prints at the uncross price; the book and chart start moving. A's **Own fills** tab shows the DEMO auction fill. |
| 0:45 | On DEMO, A: Buy 100 @ 50.00 GTC. | The bid appears in the ladder and in A's working orders. |
| 0:50 | B: Sell 40 @ 50.00. | Print on the public tape; B's order **filled**, A's **partial** (cum 40, leaves 60) in both blotters' own fills. |
| 1:00 | A: **Replace** the order to 50.10. | The original moves to Completed as **replaced**; the replacement works at 50.10 with leaves 60. |
| 1:08 | A: Replace again with quantity 30. | The core rejects it (*quantity not above filled quantity*); the reason shows on the row and the order is unchanged. |
| 1:15 | A: **Cancel** the order, then place two more and press **Cancel all**. | Cancel and the mass-cancel result line (`cancel-all (all symbols): 2 canceled`). |
| 1:22 | Reload A and log in again as alice. | The blotter is rebuilt from the FXP order-status request (see the Exec reports tab). |
| 1:30 | End. | |

Tips: enter a price with too many decimals (e.g. 50.001) to show client-side
validation before anything is sent. Start the simulator only once per take: a
second start while it runs is refused.
