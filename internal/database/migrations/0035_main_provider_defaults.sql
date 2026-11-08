-- go-pugleaf: corrections of the default providers (config.DefaultProviders)
-- LoadDefaultProviders only inserts a default that is missing by name, so rows written by
-- earlier versions keep their old values; these statements correct them. Each one matches the
-- old default exactly (name and host), so a provider the admin changed is left alone.

-- 81-171-22-215.pugleaf.net ("pugleaf Archive") does not exist any more.
DELETE FROM providers WHERE name = 'pugleaf Archive' AND host = '81-171-22-215.pugleaf.net';

-- "BlueWorldHosting Archive" pointed at news.blueworldhosting.com, BlueWorldHosting's smaller
-- public server; the archive is archive.usenet.blueworldhosting.com. The public server gets its
-- own default entry "BlueWorldHosting" (inserted by LoadDefaultProviders).
UPDATE providers SET host = 'archive.usenet.blueworldhosting.com'
 WHERE name = 'BlueWorldHosting Archive' AND host = 'news.blueworldhosting.com';

-- The two tcpreset entries allow posting; LoadDefaultProviders did not copy the posting flag,
-- so they were stored with posting = 0.
UPDATE providers SET posting = 1
 WHERE (name = 'news.tcpreset.net - Germany' AND host = 'news.tcpreset.net')
    OR (name = 'TOR: tcpreset' AND host = 'peannyjkqwqfynd24p6dszvtchkq7hfkwymi5by5y332wmosy5dwfaqd.onion');
