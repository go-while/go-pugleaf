<?php
/**
 * Single group page template
 */
$template = 'base';
?>

<div class="d-flex justify-content-between align-items-center mb-4">
    <div>
        <h1><?= h($group_name) ?></h1>
        <div class="btn-group" role="group">
            <a href="/groups/<?= urlencode($group_name) ?>" class="btn btn-sm btn-outline-primary active">
                Articles
            </a>
            <a href="/groups/<?= urlencode($group_name) ?>/threads" class="btn btn-sm btn-outline-primary">
                Threads
            </a>
        </div>
    </div>
    <div>
        <?php if (!empty($pagination)): ?>
        <small class="text-muted">
            Page <?= $pagination['current_page'] ?> of <?= $pagination['total_pages'] ?>
        </small>
        <?php endif; ?>
    </div>
</div>

<?php if (!empty($articles)): ?>
<div class="table-responsive">
    <table class="table table-hover">
        <thead>
            <tr>
                <th>#</th>
                <th>Subject</th>
                <th>From</th>
                <th>Date</th>
                <th class="text-end">Replies</th>
            </tr>
        </thead>
        <tbody>
            <?php foreach ($articles as $article): ?>
            <tr>
                <td>
                    <a href="/groups/<?= urlencode($group_name) ?>/articles/<?= (int)$article['article_num'] ?>"
                       class="text-decoration-none">
                        <?= number_format($article['article_num']) ?>
                    </a>
                </td>
                <td>
                    <a href="/groups/<?= urlencode($group_name) ?>/articles/<?= (int)$article['article_num'] ?>"
                       class="text-decoration-none">
                        <?= h($article['subject'] ?? '[No Subject]') ?>
                    </a>

                    <?php if (!empty($article['attachment_count'])): ?>
                    <span class="badge bg-secondary ms-1" title="<?= (int)$article['attachment_count'] ?> attachments">
                        📎 <?= (int)$article['attachment_count'] ?>
                    </span>
                    <?php endif; ?>
                </td>
                <td>
                    <small><?= h(extractDisplayName($article['from_header'] ?? '')) ?></small>
                </td>
                <td>
                    <small class="text-muted">
                        <?= formatRelativeTime($article['date_string'] ?? $article['date_sent']) ?>
                    </small>
                </td>
                <td class="text-end">
                    <?php if (isset($article['reply_count']) && $article['reply_count'] > 0): ?>
                    <span class="badge bg-primary"><?= number_format($article['reply_count']) ?></span>
                    <?php else: ?>
                    <span class="text-muted">0</span>
                    <?php endif; ?>
                </td>
            </tr>
            <?php endforeach; ?>
        </tbody>
    </table>
</div>

<?php if (!empty($pagination) && $pagination['total_pages'] > 1): ?>
<div class="d-flex justify-content-center">
    <?= pagination($pagination['current_page'], $pagination['total_pages'], "/groups/" . urlencode($group_name), ['page_size' => $page_size]) ?>
</div>
<?php endif; ?>

<?php else: ?>
<div class="alert alert-info">
    <h4>No Articles Found</h4>
    <p>This group doesn't have any articles yet, or they're still being fetched.</p>
    <a href="/groups" class="btn btn-primary">Browse Other Groups</a>
</div>
<?php endif; ?>
