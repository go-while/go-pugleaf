# Nginx SSL Deployment Guide

This guide covers deploying the go-pugleaf PHP frontend with Nginx, SSL certificates, and production-ready security configurations.

## Quick Start

1. **Update configuration variables** in `setup-nginx-ssl.sh`:
   ```bash
   DOMAIN="your-actual-domain.com"
   EMAIL="your-email@domain.com"
   ```

2. **Run the setup script** (as root):
   ```bash
   sudo ./setup-nginx-ssl.sh
   ```

3. **Configure your backend URL** in `/var/www/nginx/pugleaf/index.php`:
   ```php
   const API_BASE_URL = 'http://your-backend-server:port';
   ```

## Manual Setup

### Prerequisites

- Ubuntu/Debian server with root access
- Domain name pointing to your server's IP
- go-pugleaf backend running

### Step 1: Install Dependencies

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y nginx php8.2-fpm php8.2-curl php8.2-json php8.2-mbstring php8.2-xml certbot python3-certbot-nginx ufw
```

### Step 2: Configure Firewall

```bash
sudo ufw enable
sudo ufw allow 'Nginx Full'
sudo ufw allow OpenSSH
```

### Step 3: Setup Webroot

```bash
sudo mkdir -p /var/www/nginx/pugleaf
sudo cp -r php/* /var/www/nginx/pugleaf/
sudo chown -R www-data:www-data /var/www/nginx/pugleaf
sudo chmod -R 755 /var/www/nginx/pugleaf
```

### Step 4: Basic Nginx Configuration

Copy the basic configuration:
```bash
sudo cp nginx-example.conf /etc/nginx/sites-available/pugleaf
sudo ln -s /etc/nginx/sites-available/pugleaf /etc/nginx/sites-enabled/
sudo rm /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl restart nginx
```

### Step 5: Obtain SSL Certificate

```bash
sudo certbot --nginx -d your-domain.com -d www.your-domain.com
```

### Step 6: Install Production SSL Configuration

```bash
sudo cp nginx-ssl-example.conf /etc/nginx/sites-available/pugleaf-ssl
# Edit the file to replace your-domain.com with your actual domain
sudo sed -i 's/your-domain.com/your-actual-domain.com/g' /etc/nginx/sites-available/pugleaf-ssl
sudo ln -sf /etc/nginx/sites-available/pugleaf-ssl /etc/nginx/sites-enabled/pugleaf
sudo nginx -t && sudo systemctl reload nginx
```

## Configuration Files

### nginx-ssl-example.conf Features

- **SSL/TLS Configuration**: Modern TLS 1.2/1.3 with secure ciphers
- **Security Headers**: HSTS, CSP, X-Frame-Options, etc.
- **Rate Limiting**: API and general request limiting
- **Gzip Compression**: Optimized for web assets
- **Static Asset Caching**: Long-term caching for images, CSS, JS
- **PHP-FPM Integration**: Optimized FastCGI configuration
- **Error Handling**: Custom error pages through PHP
- **Access Controls**: Block sensitive directories and files

### Security Features

1. **SSL/TLS Security**:
   - TLS 1.2 and 1.3 only
   - Strong cipher suites
   - HSTS with preload
   - SSL stapling

2. **HTTP Security Headers**:
   - Content-Security-Policy
   - X-Content-Type-Options
   - X-Frame-Options
   - X-XSS-Protection
   - Referrer-Policy

3. **Access Controls**:
   - Block dotfiles and hidden directories
   - Deny access to includes/, templates/, config/
   - Prevent PHP execution in uploads
   - Block sensitive file extensions

4. **Rate Limiting**:
   - API endpoints: 10 requests/second
   - General pages: 30 requests/second
   - Burst handling with nodelay

### Performance Optimizations

1. **Caching**:
   - Static assets cached for 1 year
   - Appropriate cache headers
   - Gzip compression for text content

2. **PHP-FPM Tuning**:
   - Optimized buffer sizes
   - Extended read timeout
   - Process management tuning

3. **Connection Handling**:
   - HTTP/2 support
   - Keep-alive optimization
   - Efficient SSL session caching

## Monitoring and Maintenance

### Log Files

- Access logs: `/var/log/nginx/pugleaf_access.log`
- Error logs: `/var/log/nginx/pugleaf_error.log`
- PHP-FPM logs: `/var/log/php8.2-fpm.log`

### Health Checking

The setup script creates a health check at `/usr/local/bin/pugleaf-health-check`:
```bash
# Manual health check
sudo /usr/local/bin/pugleaf-health-check

# Check via HTTP
curl https://your-domain.com/health
```

### SSL Certificate Renewal

Certbot automatically sets up renewal. Test it:
```bash
sudo certbot renew --dry-run
```

### Useful Commands

```bash
# Test Nginx configuration
sudo nginx -t

# Reload Nginx (no downtime)
sudo systemctl reload nginx

# Restart services
sudo systemctl restart nginx php8.2-fpm

# Check SSL certificate status
sudo certbot certificates

# View real-time logs
sudo tail -f /var/log/nginx/pugleaf_*.log

# Check service status
sudo systemctl status nginx php8.2-fpm
```

## Troubleshooting

### Common Issues

1. **502 Bad Gateway**:
   - Check PHP-FPM is running: `sudo systemctl status php8.2-fpm`
   - Check socket permissions: `ls -la /var/run/php/`
   - Check PHP-FPM logs: `sudo tail -f /var/log/php8.2-fpm.log`

2. **SSL Certificate Issues**:
   - Verify DNS points to your server
   - Check firewall allows port 80/443
   - Run certbot manually: `sudo certbot --nginx -d your-domain.com`

3. **Permission Issues**:
   - Check webroot ownership: `ls -la /var/www/nginx/pugleaf/`
   - Fix permissions: `sudo chown -R www-data:www-data /var/www/nginx/pugleaf/`

4. **API Connection Issues**:
   - Verify backend is running and accessible
   - Check API_BASE_URL in `/var/www/nginx/pugleaf/index.php`
   - Test backend directly: `curl http://backend-server:port/api/groups`

### Performance Tuning

For high-traffic sites, consider:

1. **PHP-FPM Scaling**:
   ```bash
   sudo nano /etc/php/8.2/fpm/pool.d/www.conf
   # Increase pm.max_children, pm.start_servers, etc.
   ```

2. **Nginx Worker Processes**:
   ```bash
   sudo nano /etc/nginx/nginx.conf
   # Set worker_processes to number of CPU cores
   ```

3. **Enable HTTP/3** (if supported):
   ```nginx
   listen 443 quic reuseport;
   add_header Alt-Svc 'h3=":443"; ma=86400';
   ```

4. **Add Redis/Memcached** for API response caching

## Security Considerations

1. **Regular Updates**:
   ```bash
   sudo apt update && sudo apt upgrade
   sudo certbot renew
   ```

2. **Monitoring**:
   - Set up log monitoring (fail2ban, logwatch)
   - Monitor SSL certificate expiration
   - Regular security scans

3. **Backup Strategy**:
   - Backup webroot files
   - Backup Nginx configurations
   - Backup SSL certificates

4. **Additional Security**:
   - Consider Cloudflare or similar CDN/DDoS protection
   - Implement intrusion detection
   - Regular security updates
