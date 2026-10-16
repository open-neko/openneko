---
name: aw-category-rank
description: "Rank AdventureWorks product categories by sales-line revenue in a day-count window and compare the chosen category with the window before."
---

Use this skill when someone asks which product category ranks at a given position by line revenue.

1. Find the latest sales-order date. That date is the anchor.
2. Build the current window from the day count. Both endpoints count as days in the window.
3. Connect each sales-order line to its product, the product subcategory and the category.
4. Sum line revenue per category (quantity times unit price, less the discount) for orders dated in the window, then sort from highest to lowest.
5. Pick the category at the requested position and total it again for the window of equal length just before.
6. Report the category, both totals and the current window dates.
