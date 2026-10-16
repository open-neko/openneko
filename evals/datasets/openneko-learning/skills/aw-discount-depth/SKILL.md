---
name: aw-discount-depth
description: "Report the average discount on discounted AdventureWorks sales-order lines above a quantity floor in a day-count window."
---

Use this skill when someone asks how deep our line-item discounts were.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Take sales-order lines whose order date falls in the window.
4. Keep lines that received a discount and whose quantity meets the requested floor.
5. Average the discount on those lines and express it as a percentage.
6. Repeat for the window of equal length just before the current window, then report both averages and the current window dates.
