---
name: aw-freight-share
description: "Report AdventureWorks freight as a percentage of order subtotal in a day-count window that can end before the latest order date."
---

Use this skill when someone asks how large freight is compared with order subtotal.

1. Find the latest sales-order date. That date is the anchor.
2. Move the window end back from the anchor by the requested number of days. With no offset, the window ends on the anchor.
3. Build the current window from the day count. Both endpoints count as days in the window.
4. Sum freight and sum subtotal for orders in the window.
5. Divide freight by subtotal and express the result as a percentage.
6. Repeat for the window of equal length just before the current window, then report both percentages and the current window dates.
