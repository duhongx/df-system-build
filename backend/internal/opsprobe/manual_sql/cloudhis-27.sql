select  
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间"  
    ,max_conn "max_conn(最大连接数)"  
    ,now_conn "now_conn(当前连接数)"  
    ,max_conn - now_conn "remain_conn(剩余连接数)"  
from (  
    select  
         setting::int8 as max_conn  
        ,(select count(*) from pg_stat_activity ) as now_conn  
    from pg_settings  
    where name = 'max_connections'   
) a;
