---
name: aw-salesperson-rank
description: "Rank AdventureWorks salespeople by order subtotal in a day-count window and compare the chosen person with the window before."
---

Use this skill when someone asks which salesperson ranks at a given position by sales.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Total the order subtotal per salesperson in the window, then sort from highest to lowest.
4. Pick the salesperson at the requested position and look up the full name.
5. Total the same salesperson in the window of equal length just before the current window.
6. Report the full name, both totals and the current window dates.
