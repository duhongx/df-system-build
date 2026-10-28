select     
   archived_count "archived_count(已归档文件数量)",    
   last_archived_wal "last_archived_wal(最近的归档文件名称)",    
   last_archived_time "last_archived_time(最近的归档时间)",    
   failed_count "failed_count(归档失败的次数)",  
   last_failed_wal "last_failed_wal(最近的归档失败文件名称)",  
   last_failed_time "last_failed_time(最近的归档失败时间)"  
from pg_stat_archiver;
