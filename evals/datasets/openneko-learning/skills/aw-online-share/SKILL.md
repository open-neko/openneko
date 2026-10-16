---
name: aw-online-share
description: "Report the share of AdventureWorks sales orders placed online in a day-count window that can end before the latest order date."
---

Use this skill when someone asks what share of sales orders were placed online in a window.

1. Find the latest sales-order date. That date is the anchor.
2. Move the window end back from the anchor by the requested number of days. With no offset, the window ends on the anchor.
3. Build the current window from the day count. Both endpoints count as days in the window.
4. Count all orders in the window and the orders flagged as online.
5. Divide online orders by all orders and express the result as a percentage.
6. Repeat for the window of equal length just before the current window, then report both percentages and the current window dates.
