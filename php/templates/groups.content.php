<?php
/**
 * Groups listing template
 */
$template = 'base';
?>

<div class="d-flex justify-content-between align-items-center mb-4">
    <h1>Newsgroups</h1>
    <div>
        <small class="text-muted">
            <?php if (!empty($pagination)): ?>
                Showing <?= number_format($pagination['current_page']) ?> of <?= number_format($pagination['total_pages']) ?> pages
            <?php endif; ?>
        </small>
    </div>
</div>

<?php if (!empty($groups)): ?>
<div class="table-responsive">
    <table class="table table-hover">
        <thead>
            <tr>
                <th>Group Name</th>
                <th>Description</th>
                <th class="text-end">Messages</th>
                <th class="text-end">Last Activity</th>
            </tr>
        </thead>
        <tbody>
            <?php foreach ($groups as $group): ?>
            <tr>
                <td>
                    <a href="/groups/<?= urlencode($group['name']) ?>" class="text-decoration-none">
                        <strong><?= h($group['name']) ?></strong>
                    </a>
                </td>
                <td>
                    <small class="text-muted">
                        <?= h($group['description'] ?? 'No description') ?>
                    </small>
                </td>
                <td class="text-end">
                    <?= number_format($group['message_count'] ?? 0) ?>
                </td>
                <td class="text-end">
                    <small class="text-muted">
                        <?= !empty($group['last_post_date']) ? formatRelativeTime($group['last_post_date']) : 'N/A' ?>
                    </small>
                </td>
            </tr>
            <?php endforeach; ?>
        </tbody>
    </table>
</div>

<?php if (!empty($pagination) && $pagination['total_pages'] > 1): ?>
<div class="d-flex justify-content-center">
    <?= pagination($pagination['current_page'], $pagination['total_pages'], '/groups', ['page_size' => $page_size]) ?>
</div>
<?php endif; ?>

<?php else: ?>
<div class="alert alert-info">
    <h4>No Groups Found</h4>
    <p>No newsgroups are available at the moment. The backend may still be fetching data.</p>
</div>
<?php endif; ?>
