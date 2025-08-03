# Deployment and Debugging Guide

## 🔧 **API Endpoint Fixes Applied**

The original issue was that the PHP frontend was making calls to incorrect API endpoints. Here are the fixes:

### ✅ **Fixed API Endpoints**

**Before (incorrect):**
- `/api/v1/sections/api/groups` ❌ (malformed URL)
- `/newsgroups` ❌ (endpoint doesn't exist)
- `/sections` ❌ (endpoint doesn't exist)

**After (correct):**
- `/api/v1/groups` ✅ (matches Go backend)
- `/api/v1/groups/{group}/overview` ✅
- `/api/v1/groups/{group}/articles/{num}` ✅
- `/api/v1/groups/{group}/message/{messageId}` ✅
- `/api/v1/groups/{group}/threads` ✅

### ✅ **Fixed Response Data Structure**

**Before:** PHP expected `$result['newsgroups']` and `$result['articles']`
**After:** PHP now correctly uses `$result['data']` (matches Go backend response)

### ✅ **SSL/HTTPS Support**

Added proper SSL configuration to cURL requests for HTTPS backends.

## 🚀 **Deployment Steps**

1. **Copy configuration:**
   ```bash
   cp config.sample.php config.inc.php
   ```

2. **Edit config.inc.php:**
   ```php
   define('PUGLEAF_API_BASE', 'https://reader-nyc.newsdeef.eu:11980/api/v1');
   define('PUGLEAF_WEB_BASE', 'https://pugleaf.newsdeef.eu');
   define('DEBUG_MODE', false); // Set to true for debugging
   ```

3. **Upload files to server:**
   ```bash
   # Upload all PHP files to your web root
   rsync -av php/ user@server:/var/www/html/pugleaf/
   ```

4. **Test the deployment:**
   - Visit `https://pugleaf.newsdeef.eu/api-test.php` to test API connectivity
   - Visit `https://pugleaf.newsdeef.eu/ssl-debug.php` if you have SSL issues
   - Visit `https://pugleaf.newsdeef.eu/` for the main application

## 🔍 **Debugging 500 Errors**

If you still get 500 errors, follow these steps:

### Step 1: Check PHP Error Logs
```bash
# Check server error logs
tail -f /var/log/apache2/error.log  # Apache
tail -f /var/log/nginx/error.log    # Nginx
```

### Step 2: Enable Debug Mode
Edit `config.inc.php`:
```php
define('DEBUG_MODE', true);
```

### Step 3: Test API Connectivity
Access `https://pugleaf.newsdeef.eu/api-test.php` to see:
- ✅ Configuration values
- ✅ API connection test
- ✅ Sample data retrieval
- ❌ Error details if connection fails

### Step 4: Test SSL Connection
If you get SSL errors, access `https://pugleaf.newsdeef.eu/ssl-debug.php` to see:
- ✅ SSL certificate validation
- ✅ cURL connection details
- ✅ Detailed error messages

### Step 5: Check File Permissions
```bash
# Ensure web server can read files
chmod -R 644 /var/www/html/pugleaf/*.php
chmod -R 755 /var/www/html/pugleaf/
chown -R www-data:www-data /var/www/html/pugleaf/  # Adjust user as needed
```

## 🔗 **Go Backend Route Mapping**

The PHP frontend now correctly maps to these Go backend routes:

| PHP Route | Go Backend API | Description |
|-----------|---------------|-------------|
| `/` | `/api/v1/groups` | Home page (shows groups) |
| `/groups` | `/api/v1/groups` | Groups listing |
| `/groups/{group}` | `/api/v1/groups/{group}/overview` | Group articles |
| `/groups/{group}/articles/{num}` | `/api/v1/groups/{group}/articles/{num}` | Specific article |
| `/groups/{group}/message/{id}` | `/api/v1/groups/{group}/message/{id}` | Article by message ID |
| `/groups/{group}/threads` | `/api/v1/groups/{group}/threads` | Group threads |

## 📋 **Common Issues & Solutions**

### Issue: "config.inc.php not found"
**Solution:** Copy `config.sample.php` to `config.inc.php`

### Issue: SSL certificate errors
**Solution:** Update your server's CA certificates or temporarily disable SSL verification for testing

### Issue: "Connection error" or timeout
**Solution:** Verify the go-pugleaf backend is running on the specified URL/port

### Issue: "Invalid JSON response"
**Solution:** The backend might be returning HTML error pages instead of JSON. Check backend logs.

### Issue: Empty data responses
**Solution:** Ensure your go-pugleaf backend has imported newsgroup data

## 📁 **Files Added for Debugging**

- `api-test.php` - Test API connectivity and data retrieval
- `ssl-debug.php` - Diagnose SSL/HTTPS connection issues
- `config.inc.php` - Local configuration (not in git)
- `.gitignore` - Ensures config.inc.php isn't tracked

## 🎯 **Next Steps**

1. **Test the fixed deployment** on your server
2. **Run the debug scripts** if you encounter issues
3. **Check the Go backend logs** for any API errors
4. **Monitor the web server error logs** for PHP errors

The main issue was API endpoint mismatches. With these fixes, the PHP frontend should now correctly communicate with your go-pugleaf backend at `https://reader-nyc.newsdeef.eu:11980/api/v1`.
