<script>
	import { onMount } from 'svelte';
	import { GetBuildInfo } from '$lib/wailsjs/go/cmd/App';

	/** @type {import('$lib/wailsjs/go/models').cmd.BuildInfo | null} */
	let info = $state(null);
	/** @type {string} */
	let error = $state('');

	onMount(async () => {
		try {
			info = await GetBuildInfo();
		} catch (e) {
			error = String(e);
		}
	});
</script>

<main>
	<h1>CIT</h1>

	{#if error}
		<p class="error">Gagal membaca versi: {error}</p>
	{:else if info}
		<p class="version">{info.version}</p>
		<dl>
			{#if info.commit}
				<dt>Revisi</dt>
				<dd>{info.commit.slice(0, 12)}</dd>
			{/if}
			{#if info.buildDate}
				<dt>Dibangun</dt>
				<dd>{info.buildDate}</dd>
			{/if}
		</dl>
	{:else}
		<p class="version">&hellip;</p>
	{/if}
</main>

<style>
	main {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 0.25rem;
		height: 100vh;
	}

	h1 {
		margin: 0;
		font-size: 2.5rem;
		font-weight: 600;
		letter-spacing: 0.12em;
	}

	.version {
		margin: 0;
		color: var(--muted);
		font-variant-numeric: tabular-nums;
	}

	.error {
		margin: 0;
		max-width: 32rem;
		text-align: center;
		color: #e2777a;
	}

	dl {
		display: grid;
		grid-template-columns: auto auto;
		gap: 0.15rem 0.75rem;
		margin: 1.25rem 0 0;
		color: var(--muted);
		font-size: 0.8125rem;
	}

	dt {
		text-align: right;
		opacity: 0.7;
	}

	dd {
		margin: 0;
		font-family: ui-monospace, 'Cascadia Mono', 'Consolas', monospace;
	}
</style>
