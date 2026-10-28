select to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     current_database(),  
     sum(obj_num) "obj_num(对象数)"   
from (  
    select count(1) obj_num from pg_class   
    union all   
    select count(1) from pg_proc   
) a;
