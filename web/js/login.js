const $ = (id) => document.getElementById(id);

$("showPassword").addEventListener("change", (event) => {
    $("pw").type = event.target.checked ? "text" : "password";
});

$("loginForm").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = $("loginButton");
    if (button.disabled) return;
    button.disabled = true;
    button.textContent = "ログイン中…";
    event.target.setAttribute("aria-busy", "true");
    $("loginError").classList.add("hidden");
    $("loginError").textContent = "";
    try {
        let response;
        try {
            response = await fetch(Minihub.url("/api/auth/login"), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ userId: $("uid").value, password: $("pw").value }) });
        } catch (_) {
            throw new Error("通信に失敗しました。ネットワーク接続を確認して、もう一度お試しください");
        }
        const body = await response.json().catch(() => ({ error: "サーバーからの応答を読み取れませんでした" }));
        if (!response.ok) throw new Error(body.error || "ログインできませんでした");
        event.target.reset();
        $("pw").type = "password";
        const next = new URLSearchParams(location.search).get("next");
        location.replace(Minihub.scheduleReturnURL(next));
    } catch (error) {
        $("pw").value = "";
        $("pw").type = "password";
        $("showPassword").checked = false;
        $("loginError").textContent = "ログインできません: " + error.message;
        $("loginError").classList.remove("hidden");
        $("pw").focus();
        button.disabled = false;
        button.textContent = "ログイン";
    } finally {
        event.target.setAttribute("aria-busy", "false");
    }
});
