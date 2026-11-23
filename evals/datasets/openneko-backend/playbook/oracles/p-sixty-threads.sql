SELECT
  '2025-06-29'::text AS anchor_date,
  '2022-05-30'::text AS start_date,
  '0'::text AS expected_value,
  '0'::text AS baseline_value,
  NULL::text AS expected_dimension,
  jsonb_build_array(
jsonb_build_object('any_names', to_jsonb(array_remove(array[split_part((select p.name from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where pc.name = 'Bikes' and h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') group by p.name order by sum(d.orderqty) desc, p.name limit 1), ' ', 1),
    split_part((select p.name from sales.salesorderheader h join sales.salesorderdetail d using (salesorderid) join production.product p using (productid)
  left join production.productsubcategory sc using (productsubcategoryid) left join production.productcategory pc using (productcategoryid) where pc.name = 'Bikes' and h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') group by p.name order by sum(d.unitprice * (1 - d.unitpricediscount) * d.orderqty) desc, p.name limit 1), ' ', 1)]::text[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-06-30')]::numeric[], null))),
'null'::jsonb,
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.birthdate, pp.lastname limit 1),
    (select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)]::text[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from sales.salesorderheader h where true)]::numeric[], null))),
'null'::jsonb,
'null'::jsonb,
'null'::jsonb,
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and true),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and i.qty is null),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and coalesce(i.qty, 0) = 0)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and true),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and i.qty is null),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and coalesce(i.qty, 0) = 0)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and true),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and i.qty is null),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and coalesce(i.qty, 0) = 0)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and true),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and i.qty is null),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and coalesce(i.qty, 0) = 0)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and true),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and i.qty is null),
    (select count(*) from production.product p
  left join (select productid, sum(quantity) qty from production.productinventory group by 1) i using (productid)
  where p.reorderpoint > coalesce(i.qty, 0) and coalesce(i.qty, 0) = 0)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-04-01' and h.orderdate < '2025-07-01')]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select pp.lastname from humanresources.employee e join person.person pp using (businessentityid) order by e.hiredate, pp.lastname limit 1)]::text[], null))),
'null'::jsonb,
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30' group by s.name order by sum(h.subtotal) desc, s.name limit 1),
    (select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30' group by s.name order by sum(h.subtotal) desc, s.name limit 1)]::text[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where true group by s.name order by sum(h.subtotal) desc, s.name limit 1),
    (select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30' group by s.name order by sum(h.subtotal) desc, s.name limit 1),
    (select s.name from sales.salesorderheader h join sales.customer c using (customerid) join sales.store s on s.businessentityid = c.storeid where h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30' group by s.name order by sum(h.subtotal) desc, s.name limit 1)]::text[], null))),
'null'::jsonb,
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-06-29' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-06-29' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-06-28' and h.orderdate < '2025-06-29'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-06-28' and h.orderdate < '2025-06-29')]::numeric[], null))),
'null'::jsonb,
'null'::jsonb,
'null'::jsonb,
'null'::jsonb,
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct h.customerid) from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct h.customerid) from sales.salesorderheader h where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct h.customerid) from sales.salesorderheader h where h.orderdate >= '2025-01-01' and h.orderdate < '2025-04-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from sales.salesorderheader h where h.onlineorderflag)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(l.rejectedqty) from purchasing.purchaseorderheader h join purchasing.vendor v on v.businessentityid = h.vendorid join purchasing.purchaseorderdetail l using (purchaseorderid) where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(l.rejectedqty) from purchasing.purchaseorderheader h join purchasing.vendor v on v.businessentityid = h.vendorid join purchasing.purchaseorderdetail l using (purchaseorderid) where h.orderdate >= '2025-01-01' and h.orderdate < '2026-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.startdate >= '2024-01-01' and w.startdate < '2025-01-01'),
    (select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.enddate >= '2024-01-01' and w.enddate < '2025-01-01'),
    (select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.duedate >= '2024-01-01' and w.duedate < '2025-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.startdate >= '2025-01-01' and w.startdate < '2026-01-01'),
    (select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.enddate >= '2025-01-01' and w.enddate < '2026-01-01'),
    (select sum(w.scrappedqty) from production.workorder w join production.product p using (productid) left join production.scrapreason r using (scrapreasonid) where w.duedate >= '2025-01-01' and w.duedate < '2026-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct e.businessentityid) from humanresources.employee e join person.person pp using (businessentityid)
  join humanresources.employeedepartmenthistory dh using (businessentityid)
  join humanresources.department dep using (departmentid) where dep.name = 'Engineering' and dh.enddate is null)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct e.businessentityid) from humanresources.employee e join person.person pp using (businessentityid)
  join humanresources.employeedepartmenthistory dh using (businessentityid)
  join humanresources.department dep using (departmentid) where dep.name = 'Finance' and dh.enddate is null)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct e.businessentityid) from humanresources.employee e join person.person pp using (businessentityid)
  join humanresources.employeedepartmenthistory dh using (businessentityid)
  join humanresources.department dep using (departmentid) where dep.name = 'Production' and dh.enddate is null)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct e.businessentityid) from humanresources.employee e join person.person pp using (businessentityid)
  join humanresources.employeedepartmenthistory dh using (businessentityid)
  join humanresources.department dep using (departmentid) where dep.name = 'Purchasing' and dh.enddate is null)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(distinct e.businessentityid) from humanresources.employee e join person.person pp using (businessentityid)
  join humanresources.employeedepartmenthistory dh using (businessentityid)
  join humanresources.department dep using (departmentid) where dep.name = 'Sales' and dh.enddate is null)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(i.quantity) from production.productinventory i join production.location loc using (locationid) join production.product p using (productid) where loc.name = 'Finished Goods Storage')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(i.quantity) from production.productinventory i join production.location loc using (locationid) join production.product p using (productid) where loc.name = 'Paint Shop')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(i.quantity) from production.productinventory i join production.location loc using (locationid) join production.product p using (productid) where loc.name = 'Subassembly')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(i.quantity) from production.productinventory i join production.location loc using (locationid) join production.product p using (productid) where loc.name = 'Tool Crib')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Australia') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Canada') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Canada') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Canada') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Canada') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Central') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Central') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Central') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Central') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'France') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'France') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'France') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'France') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Germany') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Germany') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Germany') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Germany') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northeast') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northeast') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northeast') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northeast') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northwest') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northwest') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northwest') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Northwest') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southeast') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southeast') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southeast') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southeast') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southwest') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southwest') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southwest') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'Southwest') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'United Kingdom') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'United Kingdom') and h.orderdate >= '2025-01-01' and h.orderdate < '2025-06-30'),
    (select sum(h.subtotal) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'United Kingdom') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.territoryid = (select territoryid from sales.salesterritory where name = 'United Kingdom') and h.orderdate >= '2024-06-30' and h.orderdate < '2025-06-30')]::numeric[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select v.name from purchasing.purchaseorderheader h join purchasing.vendor v on v.businessentityid = h.vendorid where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01' group by v.name order by sum(h.subtotal) desc, v.name limit 1)]::text[], null))),
jsonb_build_object('any_names', to_jsonb(array_remove(array[(select v.name from purchasing.purchaseorderheader h join purchasing.vendor v on v.businessentityid = h.vendorid where h.orderdate >= '2025-01-01' and h.orderdate < '2026-01-01' group by v.name order by sum(h.subtotal) desc, v.name limit 1)]::text[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select count(*) from purchasing.vendor where activeflag)]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2024-11-01' and h.orderdate < '2024-11-01'::date + interval '1 month'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2024-11-01' and h.orderdate < '2024-11-01'::date + interval '1 month')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2025-02-01' and h.orderdate < '2025-02-01'::date + interval '1 month'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2025-02-01' and h.orderdate < '2025-02-01'::date + interval '1 month')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2023-01-01' and h.orderdate < '2024-01-01')]::numeric[], null))),
jsonb_build_object('values', to_jsonb(array_remove(array[(select sum(h.subtotal) from sales.salesorderheader h where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01'),
    (select sum(h.totaldue) from sales.salesorderheader h where h.orderdate >= '2024-01-01' and h.orderdate < '2025-01-01')]::numeric[], null)))
  )::text AS turn_expectations;
