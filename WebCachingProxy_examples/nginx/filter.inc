if ($request_method !~ ^(GET|POST)$ ) { return 403; }
if ($http_transfer_encoding ~* chunked) { return 403; }
if ($http_proxy) { return 403; }
if ($http_user_agent ~* "(acunetix|meta|gent|scan|gpt|amazon|rush|crawl|claude|MJ12|href|spider|go-http|python|request|harvest|extract|grab|miner|cens)") { return 403; }
if ($http_user_agent ~* "((Firefox|Chrome)/(7|8|9|10[0-9]))") { return 403; }

if ($request_uri ~* "\x" ) { return 400; }
if ($request_uri ~* "/\x") { return 400; }
if ($request_uri ~* "/wordpress") { return 400; }
if ($request_uri ~* "/wp-admin") { return 400; }

#if ($http_referer ~* "WordPress") { return 400; }


