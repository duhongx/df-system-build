select   
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     a.datname "datname(数据库名)",  
     a.pid "pid(进程id)",  
     b.rolname "username(用户名)",  
     a.application_name "app_name(应用名称)",  
     a.client_addr "client_ip(客户端ip)",  
     a.xact_start "xact_start(当前事务开始时间)",  
     a.query_start "query_start(当前查询开始时间)",  
     to_char(a.state_change,'yyyy-mm-dd hh24:mi:ss') "state_change(状态变化时间)",  
     a.state "state(状态)",  
     a.query "sql(执行的sql)",  
     a.backend_type "backend_type(后端类型)"  
from pg_stat_activity a  
inner join pg_roles b   
on (a.usesysid = b.oid)  
where a.state in ('idle in transaction','idle in transaction (aborted)')  
    and state_change < current_timestamp - interval '30 min'  
order by current_timestamp-state_change desc  
limit 5;
