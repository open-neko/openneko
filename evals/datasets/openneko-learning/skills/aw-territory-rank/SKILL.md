---
name: aw-territory-rank
description: "Rank AdventureWorks sales territories by order value in a day-count window and compare the chosen territory with the window before."
---

Use this skill when someone asks which sales territory ranks at a given position by order value.

1. Find the latest sales-order date in the connected AdventureWorks source. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Total the order value per territory in the current window, then sort from highest to lowest.
4. Pick the territory at the requested position.
5. Total the same territory in the window of equal length just before the current window.
6. Report the territory, both totals and the current window dates.
