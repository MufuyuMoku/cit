<script>
	import { formatDateTime, formatRelative } from '$lib/format.js';

	/**
	 * @type {{
	 *   tickets: import('$lib/wailsjs/go/models').cmd.TicketView[],
	 *   showAge?: boolean,
	 *   showAsset?: boolean,
	 *   busy?: number,
	 *   onClose?: (id: number) => void,
	 *   onReopen?: (id: number) => void
	 * }}
	 */
	let {
		tickets,
		showAge = false,
		showAsset = false,
		busy = 0,
		onClose,
		onReopen
	} = $props();
</script>

<ul class="tickets">
	{#each tickets as ticket (ticket.id)}
		<li class:closed={ticket.status === 'closed'}>
			<div class="thumb">
				{#if ticket.thumbUrl}
					<img src={ticket.thumbUrl} alt="" loading="lazy" />
				{:else}
					<div class="no-thumb faint">—</div>
				{/if}
			</div>

			<div class="body">
				<div class="top">
					<span class="note">{ticket.note}</span>

					{#if ticket.status === 'maybe_done'}
						<!-- A newer version suggests this may be finished. It is a
						     suggestion: nothing was closed, and nothing interrupted the
						     user to say so. -->
						<span class="tag set-aside">mungkin selesai</span>
					{/if}
					{#if ticket.status === 'closed'}
						<span class="tag">selesai</span>
					{/if}
				</div>

				<div class="meta faint">
					{#if ticket.who}
						<span>{ticket.direction === 'waiting_on_them' ? 'menunggu' : 'untuk'} {ticket.who}</span>
						·
					{/if}

					{#if showAge && ticket.status !== 'closed'}
						<!-- The number that makes someone pick up the phone. -->
						<span class="age">{ticket.ageText} tanpa kabar</span>
						·
					{/if}

					{#if showAsset}
						<a class="asset" href={`/karya/${ticket.assetId}`}>{ticket.assetName}</a>
						·
					{/if}

					<span title={`Versi yang ditunggu disimpan ${formatDateTime(ticket.versionObservedAt)}`}>
						versi {formatRelative(ticket.versionObservedAt)}
					</span>
					·
					<span class="mono">{ticket.hash}</span>

					{#if ticket.flaggedAt && ticket.status === 'maybe_done'}
						· versi baru muncul {formatRelative(ticket.flaggedAt)}
					{/if}
				</div>

				{#if onClose || onReopen}
					<div class="actions">
						{#if ticket.status === 'closed'}
							{#if onReopen}
								<button class="quiet" onclick={() => onReopen(ticket.id)} disabled={busy === ticket.id}>
									Buka lagi
								</button>
							{/if}
						{:else}
							{#if onClose}
								<button onclick={() => onClose(ticket.id)} disabled={busy === ticket.id}>
									Tandai selesai
								</button>
							{/if}
							{#if onReopen && ticket.status === 'maybe_done'}
								<button class="quiet" onclick={() => onReopen(ticket.id)} disabled={busy === ticket.id}>
									Belum, masih ditunggu
								</button>
							{/if}
						{/if}
					</div>
				{/if}
			</div>
		</li>
	{/each}
</ul>

<style>
	.tickets {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	li {
		display: flex;
		gap: 12px;
		background: var(--surface);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		padding: 10px 12px;
	}

	/* Finished work is dimmed, not hidden: it is part of what happened. */
	li.closed {
		opacity: 0.6;
	}

	.thumb {
		flex: 0 0 64px;
		width: 64px;
		height: 48px;
		background: var(--surface-sunken);
		border-radius: var(--radius-sm);
		display: flex;
		align-items: center;
		justify-content: center;
		overflow: hidden;
	}

	.thumb img {
		width: 100%;
		height: 100%;
		object-fit: contain;
	}

	.no-thumb {
		font-size: 11px;
	}

	.body {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	.top {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.note {
		font-weight: 500;
		overflow-wrap: anywhere;
	}

	.meta {
		font-size: 12px;
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.age {
		color: var(--note);
	}

	.asset {
		text-decoration: underline;
		text-decoration-color: var(--line-strong);
	}

	.actions {
		display: flex;
		gap: 8px;
		margin-top: 4px;
		flex-wrap: wrap;
	}
</style>
