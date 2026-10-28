select   
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     to_char(pg_postmaster_start_time(),'yyyy-mm-dd hh24:mi:ss') "pg_start_time(启动时间)",  
     now()-pg_postmaster_start_time() "pg_running_time(运行时长)",  
     version() "server_version(数据库版本)",  
     (case when pg_is_in_recovery()='f' then 'primary' else 'standby' end ) as  "primary_or_standby(主或备)";
