# Dictionary provenance

`FIX44.xml` is unmodified from QuickFIX v1.15.1, commit
`6908dc8c1084eeb7af7f322d35216e9965684489`, `spec/FIX44.xml`.
Its license is `LICENSE.quickfix` (SPDX `LicenseRef-QuickFIX`).
This product includes software developed by quickfixengine.org
(http://www.quickfixengine.org/).

`fxp.xml` (MIT) declares existing venue extensions (2362/2964 STP and
8013 cancel-on-disconnect), and requires Symbol within Instrument: FXP
identifies instruments by symbol, without an alternate SecurityID resolver.
Both files are embedded and compiled at process startup. No network fetch is
performed at startup. Upstream XML and extensions are separately auditable.
