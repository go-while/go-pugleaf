#!/bin/bash

# Setup script for go-pugleaf PHP frontend with Nginx and SSL
# Run as root or with sudo

set -e

# Configuration variables
DOMAIN="your-domain.com"
WEBROOT="/var/www/nginx/pugleaf"
PHP_VERSION="8.2"  # Adjust as needed
EMAIL="admin@your-domain.com"  # For Let's Encrypt

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

print_status() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   print_error "This script must be run as root or with sudo"
   exit 1
fi

# Update system
print_status "Updating system packages..."
apt update && apt upgrade -y

# Install required packages
print_status "Installing required packages..."
apt install -y nginx php${PHP_VERSION}-fpm php${PHP_VERSION}-curl php${PHP_VERSION}-json php${PHP_VERSION}-mbstring php${PHP_VERSION}-xml certbot python3-certbot-nginx ufw

# Create webroot directory
print_status "Creating webroot directory..."
mkdir -p ${WEBROOT}
chown -R www-data:www-data ${WEBROOT}

# Copy PHP files (assuming they're in the current directory)
if [ -d "php" ]; then
    print_status "Copying PHP files to webroot..."
    cp -r php/* ${WEBROOT}/
    chown -R www-data:www-data ${WEBROOT}
    chmod -R 755 ${WEBROOT}
    chmod -R 644 ${WEBROOT}/*.php
else
    print_warning "PHP directory not found. Please copy your PHP files to ${WEBROOT} manually."
fi

# Configure PHP-FPM
print_status "Configuring PHP-FPM..."
PHP_FPM_CONF="/etc/php/${PHP_VERSION}/fpm/pool.d/www.conf"
if [ -f "$PHP_FPM_CONF" ]; then
    # Backup original config
    cp "$PHP_FPM_CONF" "$PHP_FPM_CONF.backup"

    # Update PHP-FPM configuration for better performance
    sed -i 's/;pm.max_requests = 500/pm.max_requests = 1000/' "$PHP_FPM_CONF"
    sed -i 's/pm.max_children = 5/pm.max_children = 20/' "$PHP_FPM_CONF"
    sed -i 's/pm.start_servers = 2/pm.start_servers = 5/' "$PHP_FPM_CONF"
    sed -i 's/pm.min_spare_servers = 1/pm.min_spare_servers = 3/' "$PHP_FPM_CONF"
    sed -i 's/pm.max_spare_servers = 3/pm.max_spare_servers = 10/' "$PHP_FPM_CONF"
fi

# Configure firewall
print_status "Configuring firewall..."
ufw --force enable
ufw allow 'Nginx Full'
ufw allow OpenSSH

# Configure Nginx (basic config first for certbot)
print_status "Creating basic Nginx configuration..."
cat > /etc/nginx/sites-available/pugleaf << EOF
server {
    listen 80;
    listen [::]:80;
    server_name ${DOMAIN} www.${DOMAIN};

    root ${WEBROOT};
    index index.php index.html index.htm;

    location / {
        try_files \$uri \$uri/ /index.php?\$query_string;
    }

    location ~ \.php$ {
        include fastcgi_params;
        fastcgi_pass unix:/var/run/php/php${PHP_VERSION}-fpm.sock;
        fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
    }
}
EOF

# Enable site
ln -sf /etc/nginx/sites-available/pugleaf /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default

# Test Nginx configuration
print_status "Testing Nginx configuration..."
nginx -t

# Start services
print_status "Starting services..."
systemctl enable nginx php${PHP_VERSION}-fpm
systemctl restart nginx php${PHP_VERSION}-fpm

# Get SSL certificate
print_status "Obtaining SSL certificate..."
if [[ "$DOMAIN" != "your-domain.com" ]]; then
    certbot --nginx -d ${DOMAIN} -d www.${DOMAIN} --email ${EMAIL} --agree-tos --non-interactive

    # Install the full SSL configuration
    print_status "Installing SSL Nginx configuration..."
    cp nginx-ssl-example.conf /etc/nginx/sites-available/pugleaf-ssl
    sed -i "s/your-domain.com/${DOMAIN}/g" /etc/nginx/sites-available/pugleaf-ssl
    sed -i "s|/var/www/nginx/pugleaf|${WEBROOT}|g" /etc/nginx/sites-available/pugleaf-ssl
    sed -i "s/php8.2-fpm.sock/php${PHP_VERSION}-fpm.sock/g" /etc/nginx/sites-available/pugleaf-ssl

    # Replace the basic config with SSL config
    ln -sf /etc/nginx/sites-available/pugleaf-ssl /etc/nginx/sites-enabled/pugleaf

    # Test and reload
    nginx -t && systemctl reload nginx

    print_status "SSL certificate installed successfully!"
else
    print_warning "Please update the DOMAIN variable in this script before running."
    print_warning "SSL certificate not obtained. You can run 'certbot --nginx' manually later."
fi

# Set up log rotation
print_status "Setting up log rotation..."
cat > /etc/logrotate.d/pugleaf << EOF
/var/log/nginx/pugleaf_*.log {
    daily
    missingok
    rotate 14
    compress
    delaycompress
    notifempty
    create 644 www-data adm
    postrotate
        systemctl reload nginx
    endscript
}
EOF

# Create a simple health check script
print_status "Creating health check script..."
cat > /usr/local/bin/pugleaf-health-check << 'EOF'
#!/bin/bash
# Simple health check for pugleaf

# Check if Nginx is running
if ! systemctl is-active --quiet nginx; then
    echo "ERROR: Nginx is not running"
    exit 1
fi

# Check if PHP-FPM is running
if ! systemctl is-active --quiet php*-fpm; then
    echo "ERROR: PHP-FPM is not running"
    exit 1
fi

# Check if the site responds
if ! curl -sf http://localhost/health > /dev/null; then
    echo "ERROR: Site is not responding"
    exit 1
fi

echo "OK: All services are running"
EOF

chmod +x /usr/local/bin/pugleaf-health-check

# Set up a simple monitoring cron job
print_status "Setting up monitoring cron job..."
cat > /etc/cron.d/pugleaf-monitor << EOF
# Monitor pugleaf services every 5 minutes
*/5 * * * * root /usr/local/bin/pugleaf-health-check || systemctl restart nginx php${PHP_VERSION}-fpm
EOF

# Final status
print_status "Setup complete!"
echo
echo "Configuration Summary:"
echo "- Domain: ${DOMAIN}"
echo "- Webroot: ${WEBROOT}"
echo "- PHP Version: ${PHP_VERSION}"
echo "- SSL: $([ -f "/etc/letsencrypt/live/${DOMAIN}/fullchain.pem" ] && echo "Enabled" || echo "Not configured")"
echo
echo "Next steps:"
echo "1. Update your DNS records to point to this server"
echo "2. Configure your go-pugleaf backend URL in ${WEBROOT}/index.php"
echo "3. Test your site at https://${DOMAIN}"
echo "4. Monitor logs: tail -f /var/log/nginx/pugleaf_*.log"
echo
echo "Useful commands:"
echo "- Test Nginx config: nginx -t"
echo "- Reload Nginx: systemctl reload nginx"
echo "- Check SSL: certbot certificates"
echo "- Renew SSL: certbot renew"
echo "- Health check: /usr/local/bin/pugleaf-health-check"
