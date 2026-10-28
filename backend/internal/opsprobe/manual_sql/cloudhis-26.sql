select   
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     spcname AS "Name(名称)",  
     pg_catalog.pg_get_userbyid(spcowner) AS "Owner(拥有者)",  
     pg_catalog.pg_size_pretty(pg_catalog.pg_tablespace_size(oid)) AS "Size(表空间大小)"  
from pg_catalog.pg_tablespace  
order by 1;
