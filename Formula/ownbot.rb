class Ownbot < Formula
  desc "Local-first AI agent daemon and CLI"
  homepage "https://github.com/TaxCollector23/OwnBot"
  head "https://github.com/TaxCollector23/OwnBot.git", branch: "main"

  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "0"

    system "go", "build", "-trimpath", "-o", bin/"ownbot", "./cmd/ownbot"
    system "go", "build", "-trimpath", "-o", bin/"ownbotd", "./cmd/ownbotd"
  end

  test do
    assert_match "Commands:", shell_output("#{bin}/ownbot --help")
  end
end
