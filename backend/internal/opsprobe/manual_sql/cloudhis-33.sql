select   
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     a.datname "datname(数据库名)",  
     a.pid "pid(进程id)",  
     b.rolname "username(用户名)",  
     a.application_name "app_name(应用名称)",  
     a.client_addr "client_ip(客户端ip)",  
     a.query_start "query_start(当前查询开始时间)",  
     to_char(a.state_change,'yyyy-mm-dd hh24:mi:ss') "state_change(状态变化时间)",  
     a.wait_event_type "wait_event_type(等待类型)",  
     a.wait_event "wait_event(等待事件)",  
     a.state "state(状态)",  
     a.query "sql(执行的sql)",  
     a.backend_type "backend_type(后端类型)"  
from pg_stat_activity a  
left join pg_roles b   
on (a.usesysid = b.oid)  
where a.state = 'active'  
    and state_change < current_timestamp - interval '1 hour'  
    and a.datname is not null  
order by current_timestamp-state_change desc   
limit 5;
