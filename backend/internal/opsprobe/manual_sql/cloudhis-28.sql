select   
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     relname "relname(表名)",  
     b.nspname "shemaname(模式名)",  
     c.rolname "user(用户名)",  
     d.locktype "locktype(被锁对象类型)",  
     d.mode "mode(锁类型)",  
     d.pid "pid(进程id)",  
     e.query "query(锁表sql)",  
     current_timestamp-state_change "lock_duration(锁表时长)"  
from pg_class a  
inner join pg_namespace b   
on (a.relnamespace = b.oid)  
inner join pg_roles c   
on (a.relowner = c.oid)  
inner join pg_locks d  
on (a.oid = d.relation)  
left join pg_stat_activity e   
on (d.pid = e.pid)  
where d.mode = 'AccessExclusiveLock'  
order by "lock_duration(锁表时长)" desc;
