select  
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     name,  
     setting  
from  
    pg_settings a  
where a.name in (  
  'data_directory',  
  'port',  
  'client_encoding',  
  'config_file',  
  'hba_file',  
  'ident_file',  
  'archive_mode',  
  'logging_collector',  
  'log_directory',  
  'log_filename',  
  'log_truncate_on_rotation',  
  'log_statement',  
  'log_min_duration_statement',  
  'max_connections',   
  'listen_addresses'  
)  
order by name;
