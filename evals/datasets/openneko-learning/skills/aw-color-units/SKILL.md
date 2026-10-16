---
name: aw-color-units
description: "Count AdventureWorks units sold for products of one color in a day-count window and compare with the window before."
---

Use this skill when someone asks how many units of products in one color were sold.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Connect sales-order lines to their products and keep only products of the requested color.
4. Sum the ordered quantity on lines whose order date falls in the window.
5. Repeat for the window of equal length just before the current window.
6. Report both unit totals and the current window dates.
