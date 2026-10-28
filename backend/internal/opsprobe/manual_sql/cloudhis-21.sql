select   
     to_char(now(),'yyyy-mm-dd hh24:mi:ss') "巡检时间",  
     sourceline "sourceline(行号)",  
     name "para(参数名)",  
     setting "value(参数值)"  
from pg_file_settings  
order by "sourceline(行号)";
