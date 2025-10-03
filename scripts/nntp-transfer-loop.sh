#!/bin/bash
#
# Generate nntp-transfer commands for month-by-month processing starting from 1986
# Usage: ./generate_transfer_commands.sh [start_year] [end_year]
#

# Default values
START_YEAR=${1:-1980}
END_YEAR=${2:-1999}

echo "# nntp-transfer: month-by-month processing"
echo "# Start year: $START_YEAR"
echo "# End year: $END_YEAR"
echo "# started on: $(date '+%Y-%m-%d %H:%M:%S')"
echo "Live: http://199.115.116.171/rocksolid/transfer-live.txt"
echo
echo

# Function to get the number of days in a month
days_in_month() {
    local year=$1
    local month=$2
    case $month in
        1|3|5|7|8|10|12) echo 31 ;;
        4|6|9|11) echo 30 ;;
        2)
            if [ $((year % 4)) -eq 0 ] && ([ $((year % 100)) -ne 0 ] || [ $((year % 400)) -eq 0 ]); then
                echo 29  # leap year
            else
                echo 28
            fi
            ;;
    esac
}

# Generate commands for each month
for year in $(seq $START_YEAR $((END_YEAR - 1))); do
    for month in $(seq 1 12); do
        # Format month with leading zero
        month_padded=$(printf "%02d" $month)

        # Start date for this month
        start_date="${year}-${month_padded}-01"

        # Calculate end date (first day of next month)
        if [ $month -eq 12 ]; then
            next_year=$((year + 1))
            next_month="01"
            end_date="${next_year}-${next_month}-01"
        else
            next_month=$(printf "%02d" $((month + 1)))
            end_date="${year}-${next_month}-01"
        fi
        test -e ".stop" && exit 123
        # Generate the command
        echo -n "$(date) --- Processing $start_date to $end_date:"
        ./nntp-transfer -max-threads=32 -batch-check=10000 -batch-db=50000 -date-beg $start_date -date-end $end_date -host usenet.blueworldhosting.com -port 433 -ssl=false -group "\$all" -file-include newsgroups.txt -force-include-only > /var/www/html/rocksolid/transfer-live.txt 2>&1
        test $? -gt 0 && exit 1
        mv /var/www/html/rocksolid/transfer-live.txt /var/www/html/rocksolid/transfer-${year}-${month_padded}.txt
        echo " done @ http://199.115.116.171/rocksolid/transfer-${year}-${month_padded}.txt"

    done
done

echo "# Commands generated successfully"