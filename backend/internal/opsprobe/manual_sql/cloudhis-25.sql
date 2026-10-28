select   
   pid "pid(进程id)",  
   usename "username(用户名)",  
   application_name "application_name(应用名)",  
   client_addr "client_addr(IP)",  
   backend_start "backend_start(备份开始时间)",  
   state "state(WAL发送状态编码)",case   
       when state = 'startup' then '正在启动'  
       when state = 'catchup' then '追赶主库'  
       when state = 'streaming' then '流传送'  
       when state = 'backup' then '发送备份'  
       when state = 'stopping' then '发送停止'  
    end "statename(WAL状态)",  
    sync_state "sync_state(同步状态编码)",case   
       when sync_state = 'async' then '异步'  
       when sync_state = 'potential' then '后备失效变同步'  
       when sync_state = 'sync' then '同步'  
       when sync_state = 'quorum' then '候选'  
    end "sync_statename(同步状态名称)",  
    round(pg_wal_lsn_diff(pg_current_wal_lsn(),replay_lsn) /(1024 * 1024),2) as "slave_latency_mb(同步延迟_MB)"  
from pg_stat_replication;
