<?php
/**
 * Section page template - shows groups in a section
 */
$template = 'base';
?>

<div class="d-flex justify-content-between align-items-center mb-4">
    <h1>Section: <?= h($section_name) ?></h1>
    <div>
        <?php if (!empty($pagination)): ?>
        <small class="text-muted">
            Page <?= $pagination['current_page'] ?> of <?= $pagination['total_pages'] ?>
        </small>
        <?php endif; ?>
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
                    <a href="/<?= urlencode($section_name) ?>/<?= urlencode($group['name']) ?>" class="text-decoration-none">
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
    <?= pagination($pagination['current_page'], $pagination['total_pages'], "/{$section_name}", ['page_size' => $page_size]) ?>
</div>
<?php endif; ?>

<?php else: ?>
<div class="alert alert-info">
    <h4>No Groups Found</h4>
    <p>This section doesn't have any groups, or they're still being fetched.</p>
    <a href="/" class="btn btn-primary">Back to Sections</a>
</div>
<?php endif; ?>
