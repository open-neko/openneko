---
name: aw-vendor-rank
description: "Rank AdventureWorks vendors by purchase-order value in a day-count window and compare the chosen vendor with the window before."
---

Use this skill when someone asks which vendor ranks at a given position by purchase spend.

1. Find the latest purchase-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Total each vendor's purchase-order value in the window, then sort from highest to lowest.
4. Pick the vendor at the requested position.
5. Total the same vendor in the window of equal length just before the current window.
6. Report the vendor name, both totals and the current window dates.
